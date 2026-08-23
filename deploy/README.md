# Deploying the playground demo (AWS)

Runs `playground-api` + `vastfixture` + the `stitchpoint-playground`
frontend behind Caddy (automatic HTTPS via Let's Encrypt), on one small
Linux VM. `vastfixture` isn't exposed publicly — only `playground-api`
reaches it, over the private compose network — so there are exactly two
public listeners: the API and the frontend, each on its own domain.

Any Ubuntu 22.04+ VM works (EC2, Lightsail, a bare VPS); the commands
below assume EC2 since that's what this was built and tested against.

## 1. Buy two domains (or two subdomains of one)

Any registrar with no hidden markup (Cloudflare Registrar, Porkbun,
Namecheap, GoDaddy). One A record for the frontend, one for the API —
both pointing at the same instance IP (step 3). Example:
`app.example.com` (frontend) and `api.example.com` (API).

## 2. Create the instance

**Console**: EC2 → Launch instance → Ubuntu 22.04+ → **`t3.small`**
(2 vCPU / 2 GB RAM) — FFmpeg transcoding plus the Go runtime and OS
overhead makes `t3.micro`'s 1 GB a real OOM risk, not a hypothetical
one → security group allowing only 22, 80, 443 → allocate and associate
an **Elastic IP** (free while attached to a running instance; charged if
left unattached) so the domains' A records don't break on a restart.

**CLI**: see the AWS CLI commands in this project's session notes, or
run the equivalent `aws ec2 run-instances` / `create-security-group` /
`allocate-address` calls yourself — same shape as the console path
above.

## 3. Harden it and install Docker

```sh
scp bootstrap.sh ubuntu@<instance-ip>:~
ssh ubuntu@<instance-ip> 'chmod +x bootstrap.sh && ./bootstrap.sh'
```

Then, once you've confirmed SSH key login still works (test a *new*
connection before closing the one you have open):

```sh
ssh ubuntu@<instance-ip> \
  'echo "PermitRootLogin no" | sudo tee /etc/ssh/sshd_config.d/99-harden.conf \
   && sudo sshd -t && sudo systemctl restart ssh'
# then, in a NEW terminal, confirm you can still connect before trusting this
```

`PasswordAuthentication no` is already the Ubuntu cloud image default;
this just adds the same for root login explicitly. Left manual on
purpose — editing sshd config remotely and restarting the service risks
locking yourself out if something's misconfigured.

## 4. Deploy

```sh
# from your machine, at the repo root — copies this repo and the
# sibling stitchpoint-playground frontend repo, assumed checked out
# next to it (../stitchpoint-playground)
rsync -az --exclude='.git' ./ ubuntu@<instance-ip>:~/stitchpoint/
rsync -az --exclude='.git' ../stitchpoint-playground/ ubuntu@<instance-ip>:~/stitchpoint-playground/

ssh ubuntu@<instance-ip>
cd ~/stitchpoint/deploy
cp .env.example .env
vim .env   # set DOMAIN (API), FRONTEND_DOMAIN, ALLOWED_ORIGIN (= https://<FRONTEND_DOMAIN>)
cp frontend-config.js.example frontend-config.js
vim frontend-config.js   # set PLAYGROUND_API_BASE = https://<DOMAIN>
docker compose up -d --build
```

First start takes a few minutes (building the playground-api image
compiles cgo + installs ffmpeg/libav). Caddy won't get valid certs until
both domains' A records have actually propagated to this IP — check with
`dig +short <domain>` before troubleshooting a cert error.

## 5. Verify

```sh
curl -i https://<api-domain>/healthz
curl -i -X POST https://<api-domain>/api/demo
curl -i https://<frontend-domain>/
```

## Updating after a new push

```sh
# repeat the rsync commands from step 4, then:
cd ~/stitchpoint/deploy && docker compose up -d --build
```

## Logs / troubleshooting

```sh
docker compose logs -f playground-api
docker compose logs -f frontend
docker compose logs -f caddy   # cert issuance issues show up here
```
