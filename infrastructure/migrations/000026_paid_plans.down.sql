ALTER TABLE connections DROP COLUMN stopped_by_billing;
DROP TABLE plan_requests;
ALTER TABLE tenants ALTER COLUMN subscription_plan SET DEFAULT 'free';
UPDATE tenants SET subscription_plan = 'free';
UPDATE tenant_quotas SET plan_name = 'free';
DROP TABLE plan_limits;
ALTER TABLE tenants DROP COLUMN billing_status, DROP COLUMN trial_ends_at, DROP COLUMN billing_note;
