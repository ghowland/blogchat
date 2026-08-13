# SMTP Filter Server — Technical Specification

**Version:** 1.0
**Language:** Go
**Document status:** Design complete, implementation not started

---

## 1. Purpose and scope

### 1.1 Purpose

The SMTP Filter Server is a network service that receives electronic mail through the Simple Mail Transfer Protocol, admits only messages from a configured set of senders, and performs a configured action on each admitted message. The action is the execution of a local program, the transmission of an HTTP request, or the relay of the message to a downstream mail server.

### 1.2 Scope

The server is not a general purpose mail server. The following functions are outside the scope of this specification and are not implemented.

- Mailbox storage of any kind.
- Retrieval protocols such as POP3 and IMAP.
- User accounts and SMTP authentication.
- Message submission from mail clients.
- Delivery status notifications and bounce messages.
- Address enumeration commands such as `VRFY` and `EXPN`.
- Open relay for arbitrary destination domains.
- Outbound reputation management, Sender Rewriting Scheme, and DKIM signing.

### 1.3 Design constraints

The following constraints are fixed and govern every decision in this document.

1. The process performs no disk write operations. Message data exists only in memory.
2. The process requires read access to the configuration file, the TLS certificate, and the TLS private key. It requires no other filesystem access and can operate on a read-only filesystem.
3. All configuration is expressed in JSON.
4. The server does not disclose the outcome of message processing to the sending host after the message has been accepted.

---

## 2. Definitions

| Term | Definition |
|---|---|
| Peer | The remote host that opened the TCP connection. |
| Reverse-path | The address supplied in the `MAIL FROM` command. |
| Forward-path | The address supplied in the `RCPT TO` command. |
| Envelope | The reverse-path and the forward-path together. |
| Route | A configuration entry that maps a forward-path to a disposition. |
| Disposition | The action performed on an accepted message. One of command, webhook, or forward. |
| The dot | The single period on a line by itself that terminates the `DATA` stream. |
| Commit point | The moment the server transmits the reply to the dot. |
| FCrDNS | Forward-confirmed reverse DNS. |

---

## 3. Architecture

### 3.1 Process model

The process runs one goroutine per listener and one goroutine per accepted connection. One additional goroutine operates the retry queue. There is no worker pool and no other background activity.

```
main
 ├── listener goroutine (per configured listener)
 │    └── session goroutine (per connection)
 │         └── performs DNS, SPF, body read, and disposition inline
 └── queue worker goroutine (one)
```

The accept loop is written directly against `net.Listener`. Go does not supply an accept loop for raw TCP connections.

### 3.2 Shared state

Three items are shared between goroutines.

| Item | Access model |
|---|---|
| Configuration | Immutable after load. Held behind `atomic.Pointer` to permit reload. A session reads the pointer once at start and uses that value for its whole lifetime. |
| Retry queue | Protected by a `sync.Mutex`. |
| Connection semaphore | A buffered channel used to bound the number of concurrent sessions. |

No other state is shared. The session goroutine owns its envelope, its body buffer, and its selected route.

### 3.3 Package layout

```
cmd/smtpfilter/main.go     Configuration load, listener start, signal handling
internal/config/           JSON structures, validation at load time
internal/server/           Accept loop, session state machine, SMTP command handling
internal/policy/           Admission rules, resolver interface, SPF evaluation
internal/queue/            Retry queue and ticker worker
internal/dispatch/         Command, webhook, and forward dispositions
```

The `policy` package depends on a resolver interface that it declares. The production implementation wraps `net.Resolver` and the SPF library. A test implementation returns fixed answers from a map. This permits the admission rules to be tested without network access.

---

## 4. Listeners and transport security

### 4.1 Listener modes

Each configured listener has an address and a TLS mode. All listeners run the identical session handler.

| Mode | Behaviour |
|---|---|
| `plain` | No encryption. `STARTTLS` is not advertised. |
| `starttls` | The connection begins unencrypted. `STARTTLS` is advertised in the `EHLO` response. |
| `implicit` | The connection is encrypted from the first byte. `STARTTLS` is not advertised. |

The conventional assignment is port 25 with `starttls` for delivery from remote mail servers, and port 465 with `implicit` where an encrypted transport is required from the start.

### 4.2 STARTTLS handling

When the client issues `STARTTLS` and the listener mode is `starttls`, the server replies `220`, wraps the connection with `tls.Server`, and completes the handshake. On handshake failure the connection is closed without a reply.

After a successful handshake the session state is reset to the state that exists immediately after connection establishment. The client must transmit `EHLO` again. Any reverse-path or forward-path recorded before the upgrade is discarded. This reset is required by RFC 3207 and is a correctness requirement, not an option.

The session records whether the transport is encrypted. This flag is available to the admission rules and is written to the log.

### 4.3 TLS configuration

One `tls.Config` value is shared by all listeners that require it. It carries the server certificate and key, and a minimum protocol version of TLS 1.2. Client certificates are not requested and not verified.

---

## 5. SMTP protocol implementation

### 5.1 Supported commands

| Command | Behaviour |
|---|---|
| `EHLO` | Accepted. Returns the server hostname and the extension list. |
| `HELO` | Accepted. Returns the server hostname only. |
| `STARTTLS` | Accepted when the listener mode is `starttls` and the transport is not already encrypted. |
| `MAIL FROM` | Accepted. Triggers the sender admission decision defined in section 6. |
| `RCPT TO` | Accepted. Triggers the route lookup defined in section 7. |
| `DATA` | Accepted after a successful `RCPT TO`. |
| `RSET` | Accepted. Clears the envelope and the body buffer. |
| `NOOP` | Accepted. Returns `250`. |
| `QUIT` | Accepted. Returns `221` and closes the connection. |

### 5.2 Rejected commands

`AUTH`, `VRFY`, `EXPN`, `ETRN`, and `HELP` return `502 Command not implemented`. None of these are advertised in the `EHLO` response.

The extension list advertised in the `EHLO` response contains only the extensions that are implemented. This is `SIZE`, `8BITMIME`, `PIPELINING`, and `STARTTLS` where the listener mode permits it.

### 5.3 Recipient count

The server accepts a maximum of one recipient per message. A second `RCPT TO` in the same transaction returns `452 Too many recipients`. This restriction exists because the disposition is selected by the recipient, and a message with two recipients would select two dispositions with no defined ordering or failure semantics.

### 5.4 Line and stream handling

The reader enforces a maximum command line length of 512 octets, as required by RFC 5321. A longer line returns `500 Line too long` and the connection closes.

Within the `DATA` stream, a line beginning with two periods has the first period removed, which reverses the dot-stuffing applied by the sender.

A bare line feed that is not preceded by a carriage return is treated as a line terminator within the `DATA` stream and is normalised to carriage return and line feed. In the command stream, a bare line feed is rejected.

### 5.5 Timeouts

Every read operation carries a deadline. Two timeouts apply.

| Timeout | Applies to |
|---|---|
| `command_timeout_sec` | The interval between individual commands. |
| `session_timeout_sec` | The total lifetime of the connection. |

Expiry of either timeout closes the connection without a reply.

---

## 6. Sender admission

### 6.1 Evaluation point

The admission decision is made when the `MAIL FROM` command is received. Both inputs are available at that time: the peer IP address, which is known from the accepted connection, and the reverse-path, which is the argument of the command.

### 6.2 Rule order

The rules are evaluated in the following fixed order. Evaluation stops at the first rule that matches. The order does not depend on configuration.

**Rule A — CIDR whitelist.**
The peer IP address is tested for containment in each configured network. If a network contains the address, and the entry either specifies no sender domains or specifies a list that contains the domain of the reverse-path, the sender is admitted. No DNS query is performed for this rule.

**Rule B — provider whitelist.**
The FCrDNS procedure defined in section 6.3 is performed on the peer IP address. If it yields a confirmed hostname, and that hostname is equal to or is a subdomain of one of the configured suffixes in a provider entry, and the domain of the reverse-path appears in that entry's domain list, then SPF is evaluated as defined in section 6.4 and the sender is admitted on a `PASS` result.

The provider entry authorises the host. The domain list authorises which identities that host may present. The SPF evaluation confirms the authorisation against the record published by the owner of the sender domain.

This rule exists because a large mail provider transmits mail for many individual addresses that cannot be enumerated in advance.

**Rule C — address and domain whitelist.**
The reverse-path is compared against each whitelist entry using the match type of the entry. If an entry matches, SPF is evaluated and the sender is admitted on a `PASS` result.

**Rule D — default.**
The sender is rejected. The reply is `550` and the connection closes.

### 6.3 Forward-confirmed reverse DNS

The procedure has two steps and both operate under the same context deadline.

1. A PTR query on the peer IP address yields a set of hostnames.
2. For each returned hostname, an A or AAAA query yields a set of addresses.

A hostname is confirmed when the address set returned in step 2 contains the original peer IP address. The first confirmed hostname is the result. If no hostname is confirmed, the procedure fails.

This procedure demonstrates that the operator of the address block and the operator of the domain name are in agreement. It makes no statement about the reverse-path.

**MX records are not used for this purpose.** An MX record identifies the hosts that receive mail for a domain. There is no requirement that a domain transmits mail from those hosts, and large providers commonly do not. Host identity is established by FCrDNS and send authorisation is established by SPF.

### 6.4 SPF evaluation

SPF evaluation determines whether the owner of the sender domain authorises the peer IP address to transmit mail on behalf of that domain.

The implementation uses an external library. The nominated library is `github.com/mileusna/spf`, which exposes `CheckHost(ip net.IP, domain, sender, helo string) Result` and returns one of `PASS`, `FAIL`, `SOFTFAIL`, `NEUTRAL`, `NONE`, `TEMPERROR`, or `PERMERROR`. The resolver address is selected through a package-level variable that defaults to a public resolver.

This library accepts no context and exposes no resolver interface. The deadline defined in section 8 is therefore applied by an external wrapper.

```go
func checkSPF(ctx context.Context, ip net.IP, domain, sender string) spf.Result {
    ch := make(chan spf.Result, 1)
    go func() { ch <- spf.CheckHost(ip, domain, sender, "") }()
    select {
    case r := <-ch:
        return r
    case <-ctx.Done():
        return spf.TempError
    }
}
```

The channel buffer of one is mandatory. Without it, the inner goroutine blocks permanently on the send operation after a timeout and the goroutine leaks. With the buffer, the goroutine terminates when the underlying query completes and is then collected. The session is released at the deadline in both cases.

Two alternative libraries accept a context directly and permit resolver replacement, and either may be substituted if the deadline should be enforced inside the resolver rather than around it. These are `github.com/wttw/spf`, which exposes `Check(ctx, ip, sender, domain)`, and `github.com/albertito/spf`, which is used by the chasquid and maddy mail servers.

SPF evaluation is not performed when the matched entry sets `require_spf` to false. In that case the entry admits the sender on the name match alone. The flag is a property of the individual entry so that a single sender with a defective record can be excepted without relaxing the check for all senders.

### 6.5 Result mapping

| SPF result | Meaning | Action |
|---|---|---|
| `PASS` | The domain authorises the address. | Admit. |
| `FAIL` | The domain denies the address. | Reply `550`, close. |
| `SOFTFAIL` | The domain is uncertain. | Reply `550`, close. |
| `NEUTRAL` | The domain makes no assertion. | Reply `550`, close. |
| `NONE` | The domain publishes no record. | Reply `550`, close. |
| `PERMERROR` | The record is malformed. | Reply `550`, close. |
| `TEMPERROR` | A DNS query failed or timed out. | Reply `451`, close. |

`SOFTFAIL` and `NEUTRAL` are rejected because the set of legitimate senders is known in advance. An uncertain assertion carries no value in that situation.

`TEMPERROR` returns `451` and not `550`. A DNS failure is not evidence of forgery. A permanent rejection would discard valid mail during a transient network fault, and a temporary rejection causes the sending server to retry.

---

## 7. Recipient routing

### 7.1 Route lookup

The forward-path supplied in `RCPT TO` is not a mailbox. It is a key into the route table. The route table is consulted and one of two outcomes follows.

If a route matches, it is recorded on the session and the reply is `250`.

If no route matches, the reply is `550` and the connection closes.

### 7.2 Consequence

Because there is no catch-all route, an attempt to enumerate addresses produces no result, and mail addressed to any address other than the configured ones is refused before the body is transmitted.

### 7.3 Match types

A route entry matches by exact address or by domain. Where two entries could match the same forward-path, the exact address entry is selected. Comparison is case-insensitive.

---

## 8. DNS

A single `net.Resolver` value is shared by all sessions. This type is safe for concurrent use.

Every query carries a `context.Context` derived from `dns.timeout_sec`. No result is cached. A sender that opens several connections in a short interval causes one set of queries per connection.

Any query failure or deadline expiry within the FCrDNS procedure causes that procedure to fail, which causes Rule B not to match. Any query failure or deadline expiry within SPF evaluation yields `TEMPERROR`.

---

## 9. The commit point

The reply transmitted after the dot transfers responsibility for the message from the sender to the server. This is the single most important boundary in the design.

### 9.1 The disclosure rule

**Before the dot, rejection is permitted and correct.** A rejection at `MAIL FROM` or `RCPT TO` is a statement that the server will not accept mail from this sender or for this recipient. This discloses nothing that the sender does not already possess, and it prevents the transmission of a body that would be discarded.

**After the dot, the reply is always `250`.** No failure of any downstream system is reported to the sending host. The sender is informed that the message was delivered, regardless of the actual outcome. This prevents the sending host from learning anything about the internal systems behind the filter, and it prevents the sender from retrying, because retry is the responsibility of the server from this point onward.

### 9.2 Consequence

The server accepts a duty of delivery that it may fail to discharge, and it fails silently. The retry queue defined in section 11 reduces the frequency of that outcome but does not eliminate it. The log is the only record of a lost message and is therefore an operational requirement rather than a convenience.

---

## 10. Message body handling

The body is read into a byte buffer held by the session goroutine. It is released when the session ends, or transferred to the queue if the disposition fails temporarily.

The value of `max_message_bytes` is advertised in the `EHLO` response through the `SIZE` extension. It is enforced during the read of the `DATA` stream and not after.

When the limit is exceeded, the body buffer is discarded, the remainder of the stream is read and discarded until the dot is found, and the reply is `250`. A `552` reply is not transmitted, because the message has passed the commit boundary defined in section 9. The event is logged.

The product of `max_message_bytes` and `max_connections` is the worst-case memory occupied by message bodies in sessions. The value of `queue_max_bytes` is the worst-case memory occupied by the queue. The sum of these two values plus a fixed overhead is the memory budget of the process.

---

## 11. Retry queue

### 11.1 Position in the flow

The disposition is attempted once inline, while the sending host is still connected. The queue holds only what that first attempt failed to deliver. The common path therefore incurs no queue overhead.

### 11.2 Structure

```go
type Entry struct {
    From     string
    To       string
    Route    *config.Route
    Body     []byte
    Accepted time.Time
    NextTry  time.Time
    Attempts int32
}

type Queue struct {
    mu      sync.Mutex
    entries []Entry
    bytes   int64
    cfg     config.Retry
}
```

### 11.3 Worker

One goroutine operates a `time.Ticker` at `retry.interval_sec`. On each tick it acquires the mutex, copies out the entries whose `NextTry` has passed, releases the mutex, attempts each disposition, then reacquires the mutex to remove the entries that succeeded or expired.

**The mutex is not held during a disposition attempt.** A slow webhook would otherwise block every session goroutine that attempts to enqueue.

### 11.4 Removal

Removal is a swap with the final element followed by a truncation.

```go
q.bytes -= int64(len(q.entries[i].Body))
q.entries[i] = q.entries[len(q.entries)-1]
q.entries[len(q.entries)-1] = Entry{}
q.entries = q.entries[:len(q.entries)-1]
```

Order is not preserved. Order carries no meaning here, because each entry holds its own `NextTry` value and the disposition targets are independent of one another.

The assignment of the zero value to the vacated final element is required. Without it, the backing array retains a reference to the body byte slice and the garbage collector cannot reclaim it until that slot is overwritten.

When several entries are removed in one pass, iteration proceeds from the end of the slice toward the beginning.

### 11.5 Limits

An entry is not enqueued when either of the following is true.

- `len(entries)` is equal to or greater than `retry.max_entries`.
- `bytes` plus the length of the new body is greater than `retry.max_bytes`.

A refused enqueue is a discard, because the sending host has already received `250`. The event is logged at error level.

### 11.6 Expiry

An entry is removed when the interval since `Accepted` exceeds `retry.expire_sec`. This is the only stop condition. The event is logged at error level, because it represents the silent loss of a message.

### 11.7 Restart

The queue does not survive a process restart. A shutdown procedure should attempt to drain the queue before the process exits, subject to a bounded time limit.

---

## 12. Dispositions

### 12.1 Command

The configured program is executed with `exec.CommandContext`, using a context derived from the route timeout. The program is invoked directly. A shell is not used at any point.

The message body is written to the standard input of the child process. The envelope is supplied through environment variables or through fixed argument positions. Envelope values are never interpolated into a string that is then parsed.

The child process runs under a dedicated unprivileged user identity with a controlled working directory. Standard output and standard error are captured into a bounded buffer for the log.

The exit code determines the result. Zero is success. A configured value indicates a temporary failure. Any other value indicates a permanent failure.

### 12.2 Webhook

The message body is transmitted as the body of an HTTP POST request to the configured URL. The envelope is supplied in request headers.

The request body carries an HMAC signature computed with a per-route secret, so that the receiving system can authenticate the request.

The URL is taken from configuration only. It is never derived from message content. The HTTP client has a fixed timeout and does not follow redirects.

An HTTP status in the 2xx range is success. A status in the 5xx range, a connection failure, or a timeout is a temporary failure. A status in the 4xx range is a permanent failure.

### 12.3 Forward

An SMTP session is opened to the configured host and port. The original reverse-path and forward-path are presented unchanged and the body is transmitted.

The target is a mail server under the same administrative control, such as a local Postfix instance. Sender Rewriting Scheme is therefore not required, SPF alignment at the target is not a consideration, and outbound reputation is not managed. The target is expected to accept the message.

A 4xx reply or a connection failure is a temporary failure. A 5xx reply is a permanent failure.

### 12.4 Result mapping

| Disposition result | Action |
|---|---|
| Success | Reply `250`, release the body. |
| Temporary failure | Enqueue if the limits permit, otherwise discard and log. Reply `250` in both cases. |
| Permanent failure | Discard, log, reply `250`. |

---

## 13. Configuration

### 13.1 Loading

The configuration file is read once at process start and parsed into an immutable structure. Validation is performed at load time and a failure prevents the process from starting.

Reload is performed by parsing a new structure and swapping the `atomic.Pointer`. Sessions already in progress continue with the structure they began with.

### 13.2 Validation rules

- Every listener address must parse and every listener mode must be one of the three defined values.
- The certificate and key must load successfully when any listener uses `starttls` or `implicit`.
- Every CIDR entry must parse as a network.
- Every route must specify a type and the parameters required by that type.
- No two routes may specify the same recipient with the same match type.
- Every timeout and interval must be greater than zero.
- `retry.expire_sec` must be greater than `retry.interval_sec`.

### 13.3 Structure

```json
{
  "hostname": "filter.example.com",

  "listeners": [
    { "addr": ":25",  "tls": "starttls" },
    { "addr": ":465", "tls": "implicit" }
  ],

  "tls": {
    "cert": "/etc/smtpfilter/cert.pem",
    "key":  "/etc/smtpfilter/key.pem"
  },

  "limits": {
    "max_message_bytes": 10485760,
    "max_connections": 64,
    "command_timeout_sec": 60,
    "session_timeout_sec": 300
  },

  "dns": {
    "timeout_sec": 5,
    "server": "1.1.1.1:53"
  },

  "retry": {
    "enabled": true,
    "interval_sec": 60,
    "expire_sec": 3600,
    "max_entries": 500,
    "max_bytes": 268435456
  },

  "cidr_whitelist": [
    { "cidr": "10.0.0.0/8" },
    { "cidr": "203.0.113.44/32", "domains": ["vendor.example"] }
  ],

  "providers": [
    {
      "name": "google",
      "ptr_suffixes": ["google.com", "googlemail.com"],
      "domains": ["gmail.com", "googlemail.com"],
      "require_spf": true
    }
  ],

  "whitelist": [
    { "match": "address", "value": "alerts@vendor.com", "require_spf": true },
    { "match": "domain",  "value": "partner.example",   "require_spf": true },
    { "match": "domain",  "value": "legacy.example",    "require_spf": false }
  ],

  "routes": [
    {
      "recipient": "hook@filter.example.com",
      "type": "webhook",
      "url": "https://internal.example/api/mail",
      "secret": "...",
      "timeout_sec": 20
    },
    {
      "recipient": "run@filter.example.com",
      "type": "command",
      "path": "/usr/local/bin/handle",
      "args": ["--from-smtp"],
      "timeout_sec": 30,
      "temp_fail_exit_code": 75
    },
    {
      "recipient": "fwd@filter.example.com",
      "type": "forward",
      "host": "127.0.0.1",
      "port": 2525,
      "timeout_sec": 30
    }
  ]
}
```

The `dns.server` field exists only because the nominated SPF library selects its resolver through a package-level variable. An empty value leaves the library default in place.

---

## 14. Reply code summary

| Stage | Condition | Reply |
|---|---|---|
| `MAIL FROM` | No rule matches | `550`, close |
| `MAIL FROM` | SPF `FAIL`, `SOFTFAIL`, `NEUTRAL`, `NONE`, or `PERMERROR` | `550`, close |
| `MAIL FROM` | SPF `TEMPERROR` or DNS deadline expiry | `451`, close |
| `RCPT TO` | No route for the recipient | `550`, close |
| `RCPT TO` | Second recipient in one transaction | `452` |
| Final dot | Disposition succeeded | `250` |
| Final dot | Disposition failed temporarily and was enqueued | `250` |
| Final dot | Disposition failed permanently | `250`, discard, log |
| Final dot | Queue limit reached | `250`, discard, log |
| Final dot | Body exceeded the size limit | `250`, discard, log |

Every row above the dot carries a real status code. Every row at the dot is `250`.

---

## 15. Operational security

### 15.1 Privilege

Port 25 is bound either through a capability granted to the executable or through a socket supplied by the service supervisor. Privilege is dropped before the accept loop starts.

### 15.2 Resource limits

The connection semaphore bounds concurrent sessions at `max_connections`. When the limit is reached, further connections are accepted and closed immediately, or are left unaccepted in the kernel backlog.

A per-address connection limit may be added. It is not required by this specification.

### 15.3 Logging

The log is the only record of message processing and the only evidence of a message loss. Every log entry for a message carries the peer address, the reverse-path, the forward-path, the route name, and the outcome.

The following events are logged at error level because each represents a loss of a message that was accepted.

- Permanent disposition failure.
- Refused enqueue caused by a queue limit.
- Entry expiry in the queue.
- Body exceeding the size limit.

The following events are logged at warning level.

- Rejection at `MAIL FROM` with the rule that produced it and the SPF result where relevant.
- Rejection at `RCPT TO`.

A rejection recorded at warning level is the only symptom of an error in the whitelist or route configuration, because the sending host receives an ordinary rejection and reports nothing back to the operator.

### 15.4 Filesystem

The process performs no write operations. It can be run with a read-only root filesystem and no writable mount.

---

## 16. Open items

The following items are deliberately left undecided in version 1.0.

1. Whether a route may require an encrypted transport. The session already records the flag; the enforcement point and the configuration field are not defined.
2. Whether `max_message_bytes` should be configurable per route rather than globally. A per-route value cannot be enforced during the read unless the route is known, which it is, but the `SIZE` value advertised in `EHLO` precedes the route selection and would have to advertise the maximum across all routes.
3. Whether DKIM verification should be added as an optional check after the body is read.
4. Whether the shutdown procedure should drain the queue and, if so, under what time limit.

---

## 17. Implementation order

1. `internal/config` — structures and validation. Every other package reads these types.
2. `internal/policy` — resolver interface, rules A to D, result mapping. Testable with a fixed resolver.
3. `internal/server` — accept loop and session state machine.
4. `internal/dispatch` — the three dispositions.
5. `internal/queue` — slice queue and worker.
6. `cmd/smtpfilter` — assembly and signal handling.