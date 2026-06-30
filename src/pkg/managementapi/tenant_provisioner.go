package managementapi

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

const provisionQueueSize = 100

// TenantProvisioner processes async NATS provisioning jobs in the background.
type TenantProvisioner struct {
	jobs   chan ProvisionJob
	repo   Repository
	k8s    *K8sNATSProvisioner
	sseHub *TenantSSEHub
	logger *log.Logger
	quit   chan struct{}
	wg     sync.WaitGroup
}

// NewTenantProvisioner creates a new provisioner. k8s may be nil (provisioning will be skipped).
func NewTenantProvisioner(repo Repository, k8s *K8sNATSProvisioner, sseHub *TenantSSEHub, logger *log.Logger) *TenantProvisioner {
	if logger == nil {
		logger = log.Default()
	}
	return &TenantProvisioner{
		jobs:   make(chan ProvisionJob, provisionQueueSize),
		repo:   repo,
		k8s:    k8s,
		sseHub: sseHub,
		logger: logger,
		quit:   make(chan struct{}),
	}
}

// Start launches the background worker goroutine.
func (p *TenantProvisioner) Start() {
	p.wg.Add(1)
	go p.worker()
	p.logger.Printf("Tenant provisioner started (K8s available: %v)", p.k8s != nil)
}

// Stop signals the worker to stop and waits for it to drain.
func (p *TenantProvisioner) Stop() {
	close(p.quit)
	p.wg.Wait()
	p.logger.Printf("Tenant provisioner stopped")
}

// Enqueue adds a provisioning job to the queue. Returns error if queue is full.
func (p *TenantProvisioner) Enqueue(job ProvisionJob) error {
	select {
	case p.jobs <- job:
		return nil
	default:
		return fmt.Errorf("provisioning queue is full")
	}
}

func (p *TenantProvisioner) worker() {
	defer p.wg.Done()
	for {
		select {
		case job := <-p.jobs:
			p.processJob(job)
		case <-p.quit:
			// Drain remaining jobs
			for {
				select {
				case job := <-p.jobs:
					p.processJob(job)
				default:
					return
				}
			}
		}
	}
}

func (p *TenantProvisioner) processJob(job ProvisionJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	p.logger.Printf("Processing provisioning job %s for tenant %s (slug: %s)", job.JobID, job.TenantID, job.TenantSlug)

	// Mark job as running
	_ = p.repo.UpdateProvisioningJob(ctx, job.JobID, "running", 0, "Initializing...", "")

	// Broadcast initial status
	p.broadcastStatus(job.TenantID, "provisioning", 0, "Initializing...", "", "")

	// If no K8s provisioner, skip K8s and mark as active directly
	if p.k8s == nil {
		p.logger.Printf("No K8s client available, marking tenant %s as active without NATS", job.TenantID)
		_ = p.repo.UpdateProvisioningJob(ctx, job.JobID, "completed", 100, "Complete (no K8s)", "")
		_ = p.repo.UpdateTenantStatus(ctx, job.TenantID, "active", nil)
		p.broadcastStatus(job.TenantID, "active", 100, "Workspace ready", "", "")
		return
	}

	// Run K8s provisioning with progress callback
	onProgress := func(pct int, step string) {
		_ = p.repo.UpdateProvisioningJob(ctx, job.JobID, "running", pct, step, "")
		p.broadcastStatus(job.TenantID, "provisioning", pct, step, "", "")
	}

	natsUrl, err := p.k8s.ProvisionNATS(ctx, job.TenantSlug, onProgress)
	if err != nil {
		errMsg := err.Error()
		p.logger.Printf("Provisioning failed for tenant %s: %v", job.TenantID, err)
		now := time.Now().UTC()
		_ = p.repo.UpdateProvisioningJob(ctx, job.JobID, "failed", 0, "Failed", errMsg)
		_ = p.repo.UpdateProvisioningJobCompleted(ctx, job.JobID, &now)
		_ = p.repo.UpdateTenantStatus(ctx, job.TenantID, "failed", nil)
		p.broadcastStatus(job.TenantID, "failed", 0, "Provisioning failed", "", errMsg)

		// Cleanup partial K8s resources
		_ = p.k8s.DeprovisionNATS(ctx, job.TenantSlug)
		return
	}

	// Success
	natsSlug := natsResourceName(job.TenantSlug)
	now := time.Now().UTC()
	_ = p.repo.UpdateProvisioningJob(ctx, job.JobID, "completed", 100, "NATS instance ready", "")
	_ = p.repo.UpdateProvisioningJobCompleted(ctx, job.JobID, &now)
	_ = p.repo.UpdateTenantStatus(ctx, job.TenantID, "active", &natsSlug)

	// Record the instance for service discovery (#21) so workers can resolve it
	// via the API instead of a hardcoded URL.
	if store, ok := p.repo.(NATSInstanceStore); ok {
		dnsName := natsSlug + "." + tenantNATSNamespace + ".svc.cluster.local"
		if inst, rerr := store.RegisterNATSInstance(ctx, job.TenantID, 1, dnsName); rerr != nil {
			p.logger.Printf("warn: could not record NATS instance for tenant %s: %v", job.TenantID, rerr)
		} else if serr := store.SetNATSInstanceStatus(ctx, inst.ID, "active"); serr != nil {
			p.logger.Printf("warn: could not activate NATS instance %s: %v", inst.ID, serr)
		}
	}

	p.broadcastStatus(job.TenantID, "active", 100, "Workspace ready", natsUrl, "")
	p.logger.Printf("Provisioning complete for tenant %s: %s", job.TenantID, natsUrl)
}

func (p *TenantProvisioner) broadcastStatus(tenantID, status string, progress int, step, natsUrl, errMsg string) {
	if p.sseHub == nil {
		return
	}
	p.sseHub.Broadcast(tenantID, ProvisioningStatusUpdate{
		TenantID:    tenantID,
		Status:      status,
		Progress:    progress,
		CurrentStep: step,
		NATSUrl:     natsUrl,
		Error:       errMsg,
	})
}
