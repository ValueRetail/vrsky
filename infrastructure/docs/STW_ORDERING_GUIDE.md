# ServeTheWorld VPS Ordering Guide

**Quick guide to order the 3 VPS instances needed for VRSky POC**

---

## Step 1: Create ServeTheWorld Account

1. Go to: https://my.servetheworld.net/register
2. Fill in your details:
   - Email
   - Password
   - Name
   - Company (if applicable)
3. Verify your email address

---

## Step 2: Order VPS Instances

### Configure First VPS (Master Node)

1. Go to: https://my.servetheworld.net/order/vps-gp3
2. Select plan: **GP316**
   - 8 vCPU
   - 16GB RAM
   - 480GB NVMe Storage
   - 1 Gbit/s Network
   - 25TB Traffic
3. Configuration:
   - **Operating System**: Ubuntu 22.04 LTS (recommended) or AlmaLinux 9
   - **Hostname**: vrsky-master (or your choice)
   - **Location**: Oslo, Norway (default)
4. Billing Cycle: **Monthly** (for POC flexibility)
5. Add to cart

### Configure Second VPS (Worker 1)

1. Stay on the same page or return to: https://my.servetheworld.net/order/vps-gp3
2. Select plan: **GP316** (same as master)
3. Configuration:
   - **Operating System**: Ubuntu 22.04 LTS (same as master)
   - **Hostname**: vrsky-worker-1
4. Billing Cycle: **Monthly**
5. Add to cart

### Configure Third VPS (Worker 2)

1. Repeat the process one more time
2. Select plan: **GP316**
3. Configuration:
   - **Operating System**: Ubuntu 22.04 LTS
   - **Hostname**: vrsky-worker-2
4. Billing Cycle: **Monthly**
5. Add to cart

---

## Step 3: Review Cart & Apply Discount

1. Go to cart: https://my.servetheworld.net/cart
2. Review items:
   - 3× GP316 VPS
   - Total: 882 NOK/month (regular price)
3. **Apply discount code** (if available):
   - Look for promotional codes on ServeTheWorld website
   - Common: 50% discount for first 3 months
   - With discount: 441 NOK/month (~$42/month)
4. Click: **Continue to Checkout**

---

## Step 4: Complete Order

1. **Billing Information**:
   - Fill in your address
   - Company details (if applicable)
   - Tax ID (if applicable - Norway VAT)

2. **Payment Method**:
   - Credit Card (Visa/Mastercard)
   - PayPal
   - Bank Transfer (invoice)

3. **SSH Key** (IMPORTANT):
   - Upload your SSH public key OR
   - Note down the root password sent via email

4. **Review & Confirm**:
   - Verify all details
   - Accept Terms of Service
   - Click: **Complete Order**

---

## Step 5: Wait for Provisioning

**Expected Time**: 5-15 minutes

You'll receive emails for:

1. Order confirmation
2. VPS #1 ready (with IP address and access details)
3. VPS #2 ready
4. VPS #3 ready

Each email contains:

- ✅ IP Address
- ✅ Root password (if no SSH key provided)
- ✅ VPS control panel URL
- ✅ Installation instructions

---

## Step 6: Note Down IP Addresses

After receiving all 3 emails, create a file with IP addresses:

```bash
# Save to: infrastructure/vps-ips.txt
MASTER_IP=1.2.3.4
WORKER1_IP=1.2.3.5
WORKER2_IP=1.2.3.6
```

Replace with your actual IPs from emails.

---

## Step 7: Test SSH Access

```bash
# Test master
ssh root@1.2.3.4

# Test worker 1
ssh root@1.2.3.5

# Test worker 2
ssh root@1.2.3.6
```

If using password, you'll be prompted. If using SSH key, it should connect directly.

---

## Step 8: Upload SSH Key (If Not Done During Order)

If you didn't add SSH key during ordering:

```bash
# Generate key if you don't have one
ssh-keygen -t rsa -b 4096 -f ~/.ssh/vrsky_rsa

# Upload to all VPS instances
ssh-copy-id -i ~/.ssh/vrsky_rsa.pub root@1.2.3.4
ssh-copy-id -i ~/.ssh/vrsky_rsa.pub root@1.2.3.5
ssh-copy-id -i ~/.ssh/vrsky_rsa.pub root@1.2.3.6
```

---

## Cost Summary

### With 50% Discount (First 3 Months)

```
3× GP316 VPS @ 147 NOK/month each = 441 NOK/month
Approximately: $42 USD/month
```

### After Discount (Regular Price)

```
3× GP316 VPS @ 294 NOK/month each = 882 NOK/month
Approximately: $83 USD/month
```

### Total POC Cost (4 months)

```
Month 1-3: 441 NOK/month × 3 = 1,323 NOK (~$125)
Month 4:   882 NOK/month × 1 = 882 NOK (~$83)
----------------------------------------------------
Total:     2,205 NOK (~$208) for 4-month POC
```

**Note**: Monthly billing means no long-term commitment. Cancel anytime with 30-day notice.

---

## Troubleshooting

### Issue: VPS Not Provisioned After 30 Minutes

**Solution**:

1. Check email spam folder
2. Contact ServeTheWorld support: support@servetheworld.net
3. Call: +47 22 22 28 80 (Norwegian support)

### Issue: Can't Find Discount Code

**Solution**:

1. Check ServeTheWorld homepage for promotions
2. Contact sales: salg@servetheworld.net
3. Ask about first-time customer discounts

### Issue: Payment Declined

**Solution**:

1. Verify card details
2. Try PayPal instead
3. Use bank transfer (invoice) - may take 1-2 days

### Issue: Wrong Operating System Selected

**Solution**:

1. Use ServeTheWorld control panel to reinstall
2. Go to: https://my.servetheworld.net
3. Select VPS → Reinstall → Choose Ubuntu 22.04 LTS

---

## Next Steps

After VPS instances are ready:

1. ✅ Test SSH access to all 3 VPS
2. ✅ Note down all IP addresses
3. ✅ Proceed to: `infrastructure/README.md` → Quick Start
4. ✅ Run: `./scripts/provision-cluster.sh`

---

## Support Contacts

**ServeTheWorld Support**:

- Email: support@servetheworld.net
- Sales: salg@servetheworld.net
- Phone: +47 22 22 28 80
- Hours: Business hours (Norwegian time)

**VRSky GitHub**:

- Issues: https://github.com/ValueRetail/vrsky/issues
- Discussions: https://github.com/ValueRetail/vrsky/discussions

---

**Ready to order?** Start at: https://my.servetheworld.net/order/vps-gp3

**Estimated total time**: Order → Deploy → Running VRSky = ~45 minutes
