# Deployment

This directory holds the Terraform configuration that runs the blog on a
cloud provider. The blog is one Go program with one SQLite database file: a
member signs in with an email link, writes posts and chat messages, and
invites other members.

Each configuration makes one virtual machine, attaches one disk for the
database, and runs two containers on it: the blog on port 8080 and Caddy,
which gets a certificate and sends port 443 to the blog. There is no load
balancer, no Kubernetes, and no managed database.

---

## Contents

1. [Before you start](#before-you-start)
2. [What the configuration makes](#what-the-configuration-makes)
3. [Layout](#layout)
4. [Platform comparison](#platform-comparison)
5. [Caveats](#caveats)
6. [Google Cloud Platform](#google-cloud-platform)
7. [Amazon Web Services](#amazon-web-services)
8. [DigitalOcean](#digitalocean)
9. [Vultr](#vultr)
10. [Operations](#operations)
11. [Snapshots and restore](#snapshots-and-restore)
12. [Teardown](#teardown)
13. [Troubleshooting](#troubleshooting)

---

## Before you start

You need five things on your own machine, whichever platform you select.

**1. Terraform 1.5 or later.**

```bash
terraform version
```

**2. A container image in a public registry.** The instance pulls the image
with no credentials, so the image must be public. Build and push it:

```bash
docker buildx build --platform linux/amd64,linux/arm64 \
  -t ghcr.io/YOU/blog:v1.0.0 --push .
```

Then confirm that it is public. This test is important, because a private
image fails during the instance start with an error that is hard to see:

```bash
docker logout ghcr.io
docker pull ghcr.io/YOU/blog:v1.0.0
```

If that asks for a sign-in, the image is still private. Make the package
public in the registry settings and test again.

**3. A domain name that you control.** You add one A record by hand. The
configuration does not manage DNS, because a DNS provider for each of the
four platforms adds much complexity for little gain.

**4. A mail relay with a user name and a password, on port 587.** Outbound
port 25 is blocked on every cloud network in this document, so a direct
send does not work. Without working mail, nobody can sign in, including
you.

**5. The command line tool of your platform**, signed in. Each platform
section gives the commands.

---

## What the configuration makes

The same result on every platform:

| Item | Detail |
|---|---|
| One virtual machine | 1 GB of memory, Ubuntu 24.04 |
| One data disk | 10 GB, mounted at `/data`, holds the database |
| One static address | So that the DNS record stays correct |
| One firewall | Ports 80 and 443 open; port 22 limited |
| Two containers | The blog, and Caddy for the certificate |
| A daily snapshot | 03:00 UTC, kept for 28 days |

The instance startup file does this work in order: it finds the data disk,
formats it on the first boot only, mounts it at `/data`, adds an `/etc/fstab`
entry so the mount survives a reboot, installs Docker, and starts the two
containers.

Docker is configured to wait for the `/data` mount. Without that, Docker
could start first, the container would bind a directory on the boot disk,
and you would get a second empty database that disappears at the next
reboot.

---

## Layout

```
terraform/
  shared/
    cloud-init.yaml.tftpl      the instance startup file, all platforms
    snapshot-do.sh.tftpl       daily snapshot script, DigitalOcean
    snapshot-vultr.sh.tftpl    daily snapshot script, Vultr
  gcp/
  aws/
  digitalocean/
  vultr/
```

Each platform directory holds `main.tf`, `variables.tf`, `outputs.tf`, and
`terraform.tfvars.example`. You work inside one platform directory only.

There is no Terraform module wrapper. Each configuration calls the shared
startup template directly, because a module that only renders one file adds
an interface with no benefit.

---

## Platform comparison

| | GCP | AWS | DigitalOcean | Vultr |
|---|---|---|---|---|
| Instance | e2-micro | t4g.small (ARM) | s-1vcpu-1gb | vc2-1c-1gb |
| Approximate cost each month | Free tier, then about 7 USD | About 12 USD | 6 USD | 6 USD |
| Disk cost | About 1 USD | About 1 USD | 1 USD | 1 USD |
| Snapshot method | Native schedule | Native schedule | Cron on the instance | Cron on the instance |
| Access for support | IAP tunnel, no open port 22 | SSH key | SSH key | SSH key |
| Status | Tested | Tested | Tested | **Untested, see caveats** |

The costs exclude outbound traffic, which is small for a text-only site.
Check the current prices; they change.

The GCP `e2-micro` in `us-west1`, `us-central1`, or `us-east1` has fallen
under an always-free allowance. Verify the current terms before you rely
on this.

---

## Caveats

Read these before you select a platform.

**One instance only, always.** The program uses SQLite with one connection.
Two instances give two separate databases, or a corrupted one. Nothing in
these configurations scales, and that is deliberate.

**The data disk is protected from Terraform.** Every configuration sets
`prevent_destroy` on the disk. `terraform destroy` removes everything else
and then stops with an error at the disk. This is the intended behaviour:
the disk holds every member, post, and message. Removing it is a manual
action in the provider console.

**DNS must be correct before the certificate can be issued.** Caddy asks
Let's Encrypt for the certificate at the first start and the validation
uses port 80. A wrong or slow DNS record makes the request fail, and
repeated failures reach a rate limit for that name. Always confirm the
record with `dig` before you open the site.

**The Vultr configuration is not tested and has two known problems.**
First, the instance needs the block storage identifier for the snapshot
script, and the block storage needs the instance identifier for its
attachment, which is a circular dependency; the snapshot script holds a
placeholder that you must edit on the instance after the first start.
Second, Vultr snapshots have historically covered instances and not
attached block volumes, so the snapshot call may not work at all. Verify
this before you depend on it. Use GCP, AWS, or DigitalOcean unless you are
prepared to solve both.

**The geoblock is best effort.** VPN services and old allocation data make
the country wrong for some clients. The `/healthz` path is exempt, because
the platform health check must never be blocked.

**Behind the Caddy proxy, set the trusted proxy range**, or the geoblock
sees the proxy address for every request and the keys page shows the proxy
address for every session. The startup file sets `172.16.0.0/12`, which is
the Docker bridge network. This is correct for this arrangement.

---

## Google Cloud Platform

### Prepare

```bash
gcloud auth login
gcloud auth application-default login

gcloud projects create blog-prod-001 --name="Blog"
gcloud config set project blog-prod-001
gcloud billing projects link blog-prod-001 --billing-account=YOUR_BILLING_ID
gcloud services enable compute.googleapis.com
```

The billing link is necessary even for free-tier resources.

### Configure

```bash
cd terraform/gcp
cp terraform.tfvars.example terraform.tfvars
```

Edit `terraform.tfvars`:

```hcl
project_id  = "blog-prod-001"
region      = "us-west1"
zone        = "us-west1-a"
domain      = "blog.example.com"
image       = "ghcr.io/YOU/blog:v1.0.0"
site_name   = "Example"
mail_from   = "no-reply@example.com"
smtp_host   = "smtp.example.com:587"
smtp_user   = "CHANGE_ME"
smtp_pass   = "CHANGE_ME"
seed_email  = "you@example.com"
seed_handle = "root"
blocked     = "GB,AU"
```

`seed_email` and `seed_handle` make the first member. There is no
registration page, so this is the only way to make the first account.

The country codes are ISO 3166-1 alpha-2. The code for the United Kingdom
is `GB`. The code `UK` is not assigned and blocks nothing.

### Apply

```bash
terraform init
terraform plan          # expect 8 resources
terraform apply
terraform output instance_ip
```

### Point DNS at it

Create an A record for your domain at the address from the output. Then
confirm it, and do not continue until this returns the correct address:

```bash
dig +short blog.example.com
```

### Verify, layer by layer

Each command tests one layer. A failure tells you where the problem is.

```bash
Z="--zone us-west1-a --tunnel-through-iap"

# 1. The startup file finished. This blocks until it is done.
gcloud compute ssh blog $Z --command "sudo cloud-init status --wait"

# 2. The data disk is mounted. The size must be 10 GB, not the boot disk.
gcloud compute ssh blog $Z --command "df -h /data"

# 3. Both containers run.
gcloud compute ssh blog $Z --command \
  "sudo docker compose -f /opt/blog/compose.yaml ps"

# 4. The program answers. This bypasses Caddy and the firewall.
gcloud compute ssh blog $Z --command \
  "curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/healthz"

# 5. The database exists: three files.
gcloud compute ssh blog $Z --command "ls -la /data/blog.db*"

# 6. The certificate is issued.
curl -sI https://blog.example.com/healthz | head -1

# 7. The root login link.
gcloud compute ssh blog $Z --command \
  "sudo docker compose -f /opt/blog/compose.yaml logs blog | grep -A3 'ROOT LOGIN'"
```

Open the login link in a browser. You are signed in, and the keys page
appears.

### Test the property that matters

Write a post, then restart the machine:

```bash
gcloud compute instances reset blog --zone us-west1-a
sleep 90
curl -sI https://blog.example.com/healthz | head -1
```

Sign in again. The post must still be there. If it is gone, the mount
ordering failed and the container wrote to the boot disk. Check `df -h /data`
and the Docker drop-in file at
`/etc/systemd/system/docker.service.d/wait-for-data.conf`.

### Confirm the snapshot schedule

```bash
gcloud compute disks describe blog-data --zone us-west1-a \
  --format="value(resourcePolicies)"
```

The first automatic snapshot appears at the next 03:00 UTC. To test now:

```bash
gcloud compute disks snapshot blog-data --zone us-west1-a \
  --snapshot-names manual-test
gcloud compute snapshots list
gcloud compute snapshots delete manual-test
```

---

## Amazon Web Services

### Prepare

```bash
aws configure          # or export AWS_PROFILE
aws sts get-caller-identity
```

You need an SSH key pair in the target region:

```bash
aws ec2 create-key-pair --key-name blog \
  --query KeyMaterial --output text > ~/.ssh/blog.pem
chmod 600 ~/.ssh/blog.pem
```

### Configure

```bash
cd terraform/aws
cp terraform.tfvars.example terraform.tfvars
```

The AWS file adds two keys beyond the common set:

```hcl
region       = "us-west-2"
ssh_key_name = "blog"
ssh_cidr     = "203.0.113.4/32"   # your own address, not 0.0.0.0/0
```

The instance type is `t4g.small`, which is ARM. This is deliberate: it
costs less than the equivalent x86 instance, and the image build already
makes an `arm64` version. If your image is `amd64` only, change
`instance_type` to `t3.small` and change the AMI filter in `main.tf` from
`arm64` to `amd64`.

### Apply and verify

```bash
terraform init
terraform plan          # expect 10 resources
terraform apply
terraform output instance_ip
```

Create the A record, confirm it with `dig`, then:

```bash
IP=$(terraform output -raw instance_ip)
SSH="ssh -i ~/.ssh/blog.pem ubuntu@$IP"

$SSH "sudo cloud-init status --wait"
$SSH "df -h /data"
$SSH "sudo docker compose -f /opt/blog/compose.yaml ps"
$SSH "curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/healthz"
$SSH "sudo docker compose -f /opt/blog/compose.yaml logs blog | grep -A3 'ROOT LOGIN'"

curl -sI https://blog.example.com/healthz | head -1
```

### AWS-specific note on the disk

A Nitro instance renames the attached device: the requested `/dev/sdf`
appears as `/dev/nvme1n1`. The startup script waits for that path and then
falls back to a scan for a disk that has no filesystem and no partitions.
If the mount fails, check what the instance actually sees:

```bash
$SSH "lsblk"
```

### Confirm the snapshot policy

```bash
aws dlm get-lifecycle-policies --query "Policies[?State=='ENABLED']"
aws ec2 describe-snapshots --owner-ids self \
  --filters "Name=tag:Name,Values=blog-data"
```

Data Lifecycle Manager needs an IAM role, which the configuration makes.
There is no smaller arrangement on AWS.

---

## DigitalOcean

The simplest of the four, because the volume has a predictable device path
and needs no fallback logic.

### Prepare

Make an API token with read and write access in the DigitalOcean control
panel, under API, then Tokens. The same token does two jobs: Terraform uses
it, and the daily snapshot script on the instance uses it.

Add your SSH key in the control panel and get its identifier:

```bash
export DIGITALOCEAN_TOKEN=dop_v1_...
doctl compute ssh-key list
```

### Configure

```bash
cd terraform/digitalocean
cp terraform.tfvars.example terraform.tfvars
```

```hcl
do_token     = "dop_v1_..."
region       = "nyc3"
droplet_size = "s-1vcpu-1gb"
ssh_key_ids  = ["12345678"]
ssh_cidr     = "203.0.113.4/32"
```

The token is written to the instance in the snapshot script, at mode 0700.
Anyone with root on the instance can read it. If that matters to you, make
a second token with a narrower scope for the snapshot job.

### Apply and verify

```bash
terraform init
terraform apply
terraform output instance_ip
```

Create the A record, confirm with `dig`, then run the same seven checks as
the AWS section with `ssh root@$IP`. The DigitalOcean image uses `root`, not
`ubuntu`, so the `sudo` prefix is unnecessary.

### The snapshot job

DigitalOcean has no scheduled snapshot resource in its Terraform provider,
so a cron entry on the instance does the work. Test it by hand:

```bash
ssh root@$IP "/usr/local/bin/blog-snapshot"
doctl compute snapshot list
```

The script makes one snapshot named `blog-YYYYMMDD-HHMM` and removes those
older than 28 days. The output goes to `/var/log/blog-snapshot.log`.

---

## Vultr

**This configuration is not tested. Read the caveats section first.**

Two problems need your attention before the first apply.

**The circular dependency.** The instance startup file needs the block
storage identifier, and the block storage attachment needs the instance
identifier. Terraform cannot resolve both. The snapshot script therefore
ships with `PLACEHOLDER` as the block identifier. After the first apply,
find the real value and edit the file on the instance:

```bash
terraform apply
BLOCK=$(terraform output -raw block_id)
ssh root@$IP "sed -i 's/PLACEHOLDER/$BLOCK/' /usr/local/bin/blog-snapshot"
ssh root@$IP "/usr/local/bin/blog-snapshot"
```

**The snapshot may not work at all.** Vultr snapshots have historically
covered instances and not attached block volumes. If the create call
returns 404, block storage snapshots are not available and you have two
honest options:

1. Put the database on the instance disk instead of block storage, and use
   instance snapshots. This loses the separation between the machine and
   the data, so a machine rebuild loses the database.
2. Add a nightly file copy to object storage. The command
   `sqlite3 /data/blog.db ".backup /data/backup.db"` makes a clean copy of
   a live database; push that file somewhere else.

The rest of the procedure follows the DigitalOcean section.

---

## Operations

All commands below assume you can reach a shell on the instance. On GCP that
is the IAP tunnel; elsewhere it is SSH.

### Read the log

```bash
sudo docker compose -f /opt/blog/compose.yaml logs -f blog
sudo docker compose -f /opt/blog/compose.yaml logs -f caddy
```

The Caddy log is where a certificate problem appears.

### Update to a new image

Change the `image` value in `terraform.tfvars` and apply. This replaces the
instance and keeps the disk, so no data is lost. Always use a version tag.
Never point a deployment at `latest`, because a container restart would then
apply an untested version with no warning.

```bash
terraform apply
```

To update without replacing the instance:

```bash
sudo sed -i 's|blog:v1.0.0|blog:v1.1.0|' /opt/blog/compose.yaml
sudo docker compose -f /opt/blog/compose.yaml pull
sudo docker compose -f /opt/blog/compose.yaml up -d
```

Terraform then disagrees with the instance. Set the same value in
`terraform.tfvars` so that the next apply does not revert it.

### Change the site configuration

Every setting comes from an environment variable in
`/opt/blog/compose.yaml`. Edit the value in `terraform.tfvars` and apply,
which replaces the instance, or edit the file on the instance and restart
the container:

```bash
sudo docker compose -f /opt/blog/compose.yaml up -d
```

### Change the blocked countries

`BLOG_BLOCKED` holds a comma-separated list of ISO 3166-1 alpha-2 codes.
The program reads it at start, so a container restart applies the change.

### Disable a member

The program has no administration page. Set the column with the SQLite
command line tool:

```bash
sudo docker run --rm -v /data:/data alpine \
  sh -c "apk add --no-cache sqlite && \
    sqlite3 /data/blog.db \"UPDATE users SET enabled = 0 WHERE handle = 'name'\""
```

The member loses access on the next request, because the session query
reads that column. Do not delete the row: the foreign keys cascade, so a
delete removes every post and message of that member.

### Inspect the database

The blog image has no shell, so use a separate container:

```bash
sudo docker run --rm -it -v /data:/data alpine \
  sh -c "apk add --no-cache sqlite && sqlite3 /data/blog.db"
```

Useful queries:

```sql
SELECT handle, created_at, last_login FROM users;
SELECT COUNT(*) FROM posts WHERE is_chat = 0;
SELECT COUNT(*) FROM posts WHERE is_chat = 1;
```

### Make a new sign-in link by hand

If the mail relay fails and you cannot sign in, no command line tool exists
for this. The practical answer is to fix the relay. Check the log first:

```bash
sudo docker compose -f /opt/blog/compose.yaml logs blog | grep -i mail
```

---

## Snapshots and restore

Every platform takes one snapshot each day at 03:00 UTC and keeps 28 of
them. A snapshot bills for changed blocks only, so the cost after the first
one is a few cents each month.

The snapshot is crash-consistent, not clean. SQLite recovers from that
state automatically at the next start, so the snapshot is usable. For a site
with a few writes each minute, the chance of catching a write in progress is
very small.

On GCP, the snapshot policy has `KEEP_AUTO_SNAPSHOTS`, so the snapshots
survive even if somebody removes the disk by hand.

### Restore

The procedure is the same on every platform:

1. Create a new disk from the snapshot.
2. Stop the instance and detach the current disk. **Do not delete it.**
3. Attach the new disk with the same device name.
4. Start the instance.

The startup script finds a formatted disk with the label `blogdata`, mounts
it, and starts the containers. No other step is needed.

On GCP:

```bash
gcloud compute snapshots list
gcloud compute disks create blog-data-restored \
  --source-snapshot=SNAPSHOT_NAME --zone=us-west1-a

gcloud compute instances stop blog --zone us-west1-a
gcloud compute instances detach-disk blog --disk blog-data --zone us-west1-a
gcloud compute instances attach-disk blog \
  --disk blog-data-restored --device-name blog-data --zone us-west1-a
gcloud compute instances start blog --zone us-west1-a
```

Terraform now disagrees with reality, because its state names the old disk.
Import the new disk, or accept that the next apply will try to reattach the
old one. Test a restore once, on a test deployment, before you need it.

---

## Teardown

```bash
terraform destroy
```

This removes the instance, the address, the firewall, and the snapshot
policy. **It stops with an error at the data disk.** That is deliberate.

To remove the disk as well, after you are certain that you do not want the
data:

```bash
# GCP
gcloud compute disks delete blog-data --zone us-west1-a

# AWS
aws ec2 delete-volume --volume-id vol-xxxx

# DigitalOcean
doctl compute volume delete VOLUME_ID

# Vultr
vultr-cli block delete BLOCK_ID
```

The snapshots outlive the disk on GCP and AWS. Delete them separately, or
leave them; 28 snapshots of a small database cost very little.

---

## Troubleshooting

### The site does not answer at all

Work through the layers in order. The first failure names the problem.

```bash
dig +short blog.example.com          # 1. DNS points at the instance
curl -sI http://IP_ADDRESS           # 2. The instance answers on port 80
sudo cloud-init status               # 3. The startup file finished
df -h /data                          # 4. The disk is mounted
sudo docker compose -f /opt/blog/compose.yaml ps    # 5. Containers run
curl localhost:8080/healthz          # 6. The program answers
```

### The certificate does not appear

Read the Caddy log:

```bash
sudo docker compose -f /opt/blog/compose.yaml logs caddy
```

The three usual causes: the DNS record is wrong or not propagated, port 80
is not open in the firewall, or repeated earlier failures reached a Let's
Encrypt rate limit for that name. The rate limit clears after a week.

### The container restarts in a loop

```bash
sudo docker compose -f /opt/blog/compose.yaml logs blog
```

The program validates its configuration at start and names the field that
is wrong. The usual causes are a missing `BLOG_SITE_URL` or a missing
`BLOG_MAIL_FROM`.

### Nobody receives a sign-in mail

```bash
sudo docker compose -f /opt/blog/compose.yaml logs blog | grep -i mail
```

Check that `BLOG_SMTP_HOST` uses port 587 and not port 25, and that the user
name and password are correct. Port 25 is blocked outbound on every platform
in this document.

### The data disappeared after a reboot

The container wrote to the boot disk instead of the data disk. Check:

```bash
df -h /data
cat /etc/fstab | grep data
ls /etc/systemd/system/docker.service.d/
```

`/data` must be the 10 GB device. The fstab entry and the Docker drop-in
file must both exist. If `/data` is on the boot disk, the database there is
the one in use; copy it to the correct location before you repair the mount.

### The startup file failed

```bash
sudo cat /var/log/cloud-init-output.log
```

This holds every command and its output, including the disk detection and
the image pull. A failed image pull here almost always means the image is
private.
