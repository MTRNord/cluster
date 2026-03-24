# Phorge Inbound Email

Documents the inbound email setup for Phorge at https://phorge.mtrnord.blog.

## How it works

Phorge generates reply-to addresses like `phorge+T123+hash@phorge.mtrnord.blog`. When a user
replies to a Phorge notification, the reply flows:

```
Reply email → MX phorge.mtrnord.blog → email-gateway LB (cluster)
                                               ↓
                                     Stalwart (relay rule for phorge.mtrnord.blog)
                                               ↓ relays via Hetzner private network
                                     Postfix on dns-a (10.0.64.129:25)
                                               ↓ virtual alias → pipe
                                     /var/www/html/git/phorge/scripts/mail/mail_handler.php
                                               ↓
                                     Phorge DB — comment created
```

## Cluster config (gitops)

Stalwart routes all mail for `phorge.mtrnord.blog` to `10.0.64.129:25` via a relay rule in
`apps/talos_cluster/stalwart/release.yaml`:

```yaml
queue:
  route:
    phorge-relay:
      type: relay
      address: 10.0.64.129
      port: 25
      protocol: smtp
      tls:
        implicit: false
        allow-invalid-certs: true
  strategy:
    route:
      - if: "rcpt_domain == 'phorge.mtrnord.blog'"
        then: "'phorge-relay'"
```

## dns-a config (10.0.64.129)

Postfix accepts mail for `phorge.mtrnord.blog` and pipes it to `mail_handler.php`.

**`/etc/postfix/main.cf` relevant settings:**

```
mydestination = localhost
virtual_alias_domains = phorge.mtrnord.blog
virtual_alias_maps = hash:/etc/postfix/virtual
mynetworks = 127.0.0.0/8 10.0.64.0/19
smtpd_relay_restrictions = permit_mynetworks, reject
smtpd_recipient_restrictions = permit_mynetworks, reject
inet_interfaces = 127.0.0.1, 10.0.64.129
inet_protocols = ipv4
```

**`/etc/postfix/virtual`:**

```
phorge@phorge.mtrnord.blog  phorge-pipe
```

**`/etc/aliases`:**

```
phorge-pipe: "|/var/www/html/git/phorge/scripts/mail/mail_handler.php"
```

**Phorge config:**

```
metamta.reply-handler-domain = phorge.mtrnord.blog
metamta.single-reply-handler-prefix = phorge
```

## DNS

```
phorge.mtrnord.blog.  MX  10  <email-gateway-lb-ip>
phorge.mtrnord.blog.  TXT  "v=spf1 mx include:midnightthoughts.space ~all"
```

## Troubleshooting

**Test the pipe directly on dns-a:**

```bash
echo "From: test@example.com
To: phorge@phorge.mtrnord.blog
Subject: Test
Message-ID: <test-$(date +%s)@example.com>

test body" | sudo /var/www/html/git/phorge/scripts/mail/mail_handler.php
```

**Check Phorge received it:**

```bash
cd /var/www/html/git/phorge && ./bin/mail list-inbound
```

**Check Postfix queue on dns-a:**

```bash
sudo mailq
sudo postcat -q <queue-id>
```

**Check Stalwart relay delivery in pod logs:**

```bash
kubectl logs -n stalwart -l app.kubernetes.io/name=stalwart-mail | grep phorge
```
