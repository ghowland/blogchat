# blogchat — Technical Specification

**Version 1.0** · A minimal invite-only blog and chat platform

---

## Table of contents

1. [What blogchat is](#1-what-blogchat-is)
2. [Design principles](#2-design-principles)
3. [Concepts](#3-concepts)
4. [Architecture](#4-architecture)
5. [Data model](#5-data-model)
6. [Authentication](#6-authentication)
7. [Session keys](#7-session-keys)
8. [Invitations](#8-invitations)
9. [Posts](#9-posts)
10. [Chat](#10-chat)
11. [Geoblocking](#11-geoblocking)
12. [HTTP surface](#12-http-surface)
13. [Rendering](#13-rendering)
14. [Client-side behaviour](#14-client-side-behaviour)
15. [Security model](#15-security-model)
16. [Configuration](#16-configuration)
17. [Source layout](#17-source-layout)
18. [Runtime behaviour](#18-runtime-behaviour)
19. [Deployment](#19-deployment)
20. [Operations](#20-operations)
21. [Limits and constants](#21-limits-and-constants)
22. [Known limitations](#22-known-limitations)
23. [Glossary](#23-glossary)

---

## 1. What blogchat is

blogchat is a text-only, invite-only, private community site. It has two areas: a blog where members write posts with a subject and a body, and a chat where members send short messages in named channels. Both areas require a sign-in to read as well as to write; nothing is public.

The whole system is one Go program and one SQLite database file. There are no passwords, no user database to protect, no registration page, no external services at runtime, and no framework.

**Who it is for.** A small closed group — a family, a club, a project team, a set of friends — that wants a private place to write and talk without an account at a large platform. It is designed for tens of members, not thousands.

**What it is not.** It is not a public blog, not a forum with anonymous readers, not a Slack replacement with file uploads and notifications, and not a system that scales horizontally. Each of those is a deliberate exclusion, not a missing feature.

---

## 2. Design principles

These five principles explain nearly every decision in this document. When a later section seems restrictive, the reason is usually here.

**One process, one file.** All state lives in one SQLite database on one disk, accessed by one process through one connection. This removes every class of distributed-systems problem: no cache invalidation between machines, no distributed locking, no connection pool contention, no database server to operate. The cost is that the system cannot run two instances.

**No passwords.** The system never stores, transmits, or verifies a password. Identity is proven by control of an email address. This removes password storage, password reset flows, credential stuffing, and the responsibility of protecting a password database.

**Text only.** Posts and messages are plain text. There is no Markdown, no HTML, no image upload, no file attachment. This removes an entire category of injection risk and keeps the database small.

**Server-rendered HTML.** Pages are complete HTML documents built on the server. There is no client-side framework, no JSON API for the browser, and no build step for the front end. Interactivity is added by HTMX attributes on the HTML itself.

**Everything in one binary.** Templates, style sheet, and client script are compiled into the executable with Go's `embed` feature. Deployment is one file plus one database. There is no assets directory to synchronise and no static file server to configure.

---

## 3. Concepts

New readers should understand these seven terms before the rest of the document.

**Member.** A person with an account. Every member has an email address, which is private and never shown, and a handle, which is public and shown on everything they write.

**Handle.** The public display name, 2 to 24 characters, letters and digits and the signs `_`, `-`, `.`. It is set when the member is invited and cannot be changed afterwards. The email address is never displayed anywhere in the interface.

**Invitation.** The only way to join. An existing member enters an email address and a handle; this creates the account immediately and sends a sign-in link. There is no registration page and no invitation code to redeem.

**Sign-in link.** A one-time URL sent by email, valid for 15 minutes. Opening it creates a session. This is the only authentication mechanism.

**Key.** A session. Each device that signs in gets its own key. A member can see all their keys and remove every key except the current one, which forces the other devices to sign in again.

**Post.** A blog thread with a subject and a body. The member who wrote it owns it and can delete it. Other members write replies, which have a body only.

**Channel.** A chat room with a name and an optional topic. Messages inside it are short and the channel keeps only the newest 500; older messages are deleted permanently.

A post and a channel are the same kind of database row distinguished by one flag. A reply and a chat message are likewise the same kind of row. This is explained in section 5.

---

## 4. Architecture

### 4.1 The whole system

```
                     Internet
                        │
                    443 │ HTTPS
                        ▼
               ┌─────────────────┐
               │      Caddy      │  certificate, TLS termination
               └────────┬────────┘
                        │ 8080 HTTP
                        ▼
               ┌─────────────────┐
               │  blogchat (Go)  │  one process
               │                 │
               │  geoblock       │  middleware, runs first
               │  router         │  net/http ServeMux
               │  handlers       │  session check, then the work
               │  templates      │  mustache, embedded
               └────────┬────────┘
                        │ one connection
                        ▼
               ┌─────────────────┐
               │  /data/blog.db  │  SQLite, WAL mode
               └─────────────────┘
```

Caddy is a separate container that obtains and renews a TLS certificate from Let's Encrypt automatically and forwards decrypted requests to the program. The program itself speaks plain HTTP and knows nothing about certificates.

### 4.2 Request path

Every request passes through the same sequence:

1. **Geoblock middleware.** Determines the country of the client and returns 403 if it is on the block list. This runs before the router and before any database access, so a blocked request costs almost nothing. The path `/healthz` is exempt.
2. **Request log.** One line per request. Sign-in link paths are rewritten to `/l/[token]` so the secret never reaches the log.
3. **Router.** Go's standard `ServeMux` with method and wildcard patterns, available since Go 1.22. No third-party router.
4. **Authentication wrapper.** All routes except five require a valid session. The wrapper looks up the session, computes a CSRF token, and passes both to the handler.
5. **Handler.** Validates input, calls the database, renders a template.
6. **Last-seen update.** After the response, the session's last-seen time is written, at most once per minute per session.

### 4.3 Concurrency

Go's HTTP server handles each request in its own goroutine. The database has one connection with `SetMaxOpenConns(1)`, so all database access is serialised by the connection pool. This means:

- No `SQLITE_BUSY` errors are possible.
- No transaction can deadlock with another.
- Database access is a queue; a slow query blocks others.

For the intended scale — tens of members, queries that touch a few indexed rows — the serialisation is not a bottleneck. Every query in the system completes in well under a millisecond.

Shared in-memory state uses `atomic.Pointer` for the configuration and the geo table (read on every request, replaced rarely) and a mutex-protected map for rate limits.

---

## 5. Data model

Five tables. All identifiers are 64-bit integers; all timestamps are Unix seconds.

### 5.1 users

```sql
CREATE TABLE users (
    id          INTEGER PRIMARY KEY,
    email       TEXT    NOT NULL UNIQUE,   -- private, never rendered
    handle      TEXT    NOT NULL UNIQUE,   -- public
    invited_by  INTEGER REFERENCES users(id),
    created_at  INTEGER NOT NULL,
    last_login  INTEGER,                   -- NULL until first sign-in
    enabled     INTEGER NOT NULL DEFAULT 1
);
```

`invited_by` is NULL for exactly one row: the root member, created at first startup. The interface shows "founder" for that member.

`last_login` serves two purposes: it records the first successful sign-in, and its NULL state marks an invitation as still open for the quota calculation.

`enabled` set to 0 revokes access on the next request. This is the correct way to remove a member; deleting the row cascades and destroys all their content.

### 5.2 login_tokens

```sql
CREATE TABLE login_tokens (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  BLOB    NOT NULL UNIQUE,   -- SHA-256 of 32 random bytes
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    used_at     INTEGER                    -- NULL until consumed
);
```

The raw token exists only in the email. The database stores its hash. A token is single-use and time-limited.

### 5.3 sessions

```sql
CREATE TABLE sessions (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  BLOB    NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    ip          TEXT    NOT NULL,
    agent       TEXT    NOT NULL           -- truncated to 120 characters
);
```

`ip` and `agent` exist solely so the member can recognise their own devices on the keys page.

### 5.4 posts

```sql
CREATE TABLE posts (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subject     TEXT    NOT NULL,
    body        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    is_chat     INTEGER NOT NULL DEFAULT 0,
    last_at     INTEGER NOT NULL DEFAULT 0
);
```

**This table holds both blog posts and chat channels.** The distinction:

| Column | Blog post (`is_chat = 0`) | Channel (`is_chat = 1`) |
|---|---|---|
| `subject` | The post title | The channel name |
| `body` | The post text | The topic line, may be empty |
| `created_at` | Sort key for the feed | Creation time |
| `last_at` | Unused | Time of newest message; sort key for the channel list |

`last_at` is separate from `updated_at` deliberately. `updated_at` means "the post text changed"; `last_at` means "activity occurred". A reply does not change the post text.

The reason `last_at` exists as a column rather than being computed: the channel list sorts by most recent activity. Computing that with a correlated subquery would prevent SQLite from using an index for the sort. With the column, the sort is a single index scan.

### 5.5 replies

```sql
CREATE TABLE replies (
    id          INTEGER PRIMARY KEY,
    post_id     INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    body        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
);
```

Holds both blog replies and chat messages. Which it is depends on the `is_chat` flag of its parent post. Blog replies are kept forever; chat messages are trimmed to the newest 500 per channel.

### 5.6 Indexes

```sql
CREATE INDEX idx_sessions_user   ON sessions(user_id);
CREATE INDEX idx_tokens_user     ON login_tokens(user_id);
CREATE INDEX idx_replies_post    ON replies(post_id, id);
CREATE INDEX idx_posts_kind_time ON posts(is_chat, created_at DESC);
CREATE INDEX idx_posts_kind_last ON posts(is_chat, last_at DESC);
CREATE INDEX idx_users_inviter   ON users(invited_by);
```

`idx_replies_post` on `(post_id, id)` is used by three operations: listing a thread's replies, fetching new chat messages after a given id, and the chat trim. One index serves all three.

### 5.7 Schema versioning

The schema is applied from a `migrations` slice, with `PRAGMA user_version` recording the position. Each migration runs in its own transaction. Entries are never edited after release; changes are added as new entries.

### 5.8 The shared-table design

Sharing `posts` and `replies` between blog and chat is the central structural decision. It gives one insert path, one ownership rule, one delete-cascade, and one set of indexes.

Its risk: a query that forgets the `is_chat` filter mixes the two areas. The following functions must always filter:

- `ListPosts(chat bool, ...)` — filters
- `CountPosts(chat bool)` — filters
- `GetPost(pid)` — deliberately does **not** filter, because the handler needs to know which kind it found in order to redirect

Handlers check `IsChat` and redirect: `/p/{id}` on a channel redirects to `/c/{id}`, and `/c/{id}` on a post redirects to `/p/{id}`. A wrong link self-corrects rather than producing a confusing error.

---

## 6. Authentication

### 6.1 The model

Identity is proven by control of an email address, once per session. There is no password at any point.

### 6.2 Sign-in flow

```
1. Visitor opens /
       ↓
2. Enters email address, submits POST /login
       ↓
3. Server looks up the address
   ├── found and enabled → create token, send mail
   └── not found         → do nothing
       ↓
4. Server shows the same confirmation in both cases
       ↓
5. Member opens https://site/l/<token> from the mail
       ↓
6. Server verifies: hash matches, not used, not expired, member enabled
       ↓
7. Server marks token used, sets last_login, creates session, sets cookie
       ↓
8. Redirect to /keys
```

**Step 4 is a security property, not an oversight.** An identical response for known and unknown addresses prevents the page from disclosing who is a member.

**Step 8 is a deliberate user-experience choice.** Landing on the keys page means every sign-in shows the member which devices have access, making an unrecognised session visible immediately.

### 6.3 Token handling

Both login tokens and session tokens are generated the same way:

```go
buf := make([]byte, 32)
rand.Read(buf)                      // crypto/rand
digest := sha256.Sum256(buf)
raw := base64.RawURLEncoding.EncodeToString(buf)
```

The raw value goes to the member; the database stores only the digest. To verify, the server hashes the presented value and looks it up by hash — a single unique-index lookup. Because the lookup is by hash equality in the database, no constant-time comparison is needed in Go.

If `crypto/rand` fails, the program panics. It cannot produce a safe token in that state and must not continue.

### 6.4 Token consumption

Consuming a login token is a transaction where the state check is inside the `UPDATE`:

```sql
UPDATE login_tokens SET used_at = ?
WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?
```

If zero rows are affected, the token was already used or has expired. The single-connection design makes a race impossible anyway, but the condition stays in the statement so that a future change to the connection limit cannot introduce a defect.

### 6.5 Session cookie

```
Set-Cookie: sid=<raw token>; Path=/; Max-Age=2592000;
            HttpOnly; Secure; SameSite=Lax
```

`Secure` is set only when the configured site URL uses HTTPS, so local HTTP testing works. `HttpOnly` prevents script access. `SameSite=Lax` is a second layer of CSRF defence beneath the token described in section 15.

### 6.6 Reading a session

On each authenticated request: read the cookie, decode and hash it, then:

```sql
SELECT ses.id, <user columns>
FROM sessions ses
JOIN users usr ON usr.id = ses.user_id
LEFT JOIN users inv ON inv.id = usr.invited_by
WHERE ses.token_hash = ? AND ses.expires_at > ? AND usr.enabled = 1
```

The `enabled = 1` condition means disabling a member revokes every one of their sessions instantly, with no separate revocation step.

### 6.7 Rate limiting

`POST /login` is limited to 5 requests per 15 minutes, counted separately per email address and per client IP. This prevents using the site to flood someone's inbox. The sign-in link path `/l/{token}` is limited to 20 per 15 minutes per IP.

Limits are held in an in-memory map with a fixed-window counter, cleaned every 5 minutes. This is correct because there is exactly one process.

---

## 7. Session keys

### 7.1 Purpose

Each device that signs in creates a separate session row — a "key". Because a sign-in link works on any device that opens it, a leaked link creates a session the member did not intend. The keys page makes this visible and reversible.

### 7.2 The keys page

`GET /keys` lists every active session of the member, newest first:

| Signed in | Last seen | Address | Device | |
|---|---|---|---|---|
| 2026-08-11 09:22 | 2026-08-11 10:47 | 203.0.113.4 | Mozilla/5.0 (Macintosh...) | this device |
| 2026-08-09 14:03 | 2026-08-09 14:10 | 198.51.100.9 | Mozilla/5.0 (iPhone...) | |

The page also shows the member's handle, who invited them, and when they joined.

### 7.3 Removing other keys

`POST /keys/revoke-others` executes:

```sql
DELETE FROM sessions WHERE user_id = ? AND id <> ?
```

The current session is kept; every other device loses access on its next request and is redirected to the sign-in page. This is the recovery action for a leaked link, a shared computer, or a lost device.

`POST /logout` removes only the current session.

### 7.4 Last-seen updates

Writing `last_seen` on every request would mean a database write per page view. Instead, a `SeenCache` holds the last write time per session in memory and permits a write at most once per 60 seconds per session. The displayed time is therefore accurate to within a minute, which is sufficient for its purpose.

---

## 8. Invitations

### 8.1 The model

Creating a member *is* the invitation. There is no pending-invitation table, no code to redeem, and no acceptance step.

An existing member submits an email address and a handle. The server creates the `users` row immediately with `invited_by` set to the inviter, creates a login token valid for 7 days, and sends a mail containing the handle and the sign-in link.

### 8.2 Quota

Each member may have at most 5 *open* invitations, where open means `last_login IS NULL` — the invited person has never signed in. Completing a first sign-in frees a slot. This limits the damage from a compromised account without preventing normal growth.

### 8.3 Duplicate handling

The `email` and `handle` columns are both unique. A collision produces a constraint error, and the interface reports "that address or that handle is not available" without distinguishing which — so the invite form cannot be used to enumerate members.

### 8.4 Bootstrap

The first member cannot invite themselves. At startup, if the `users` table is empty, the program reads a seed email and handle from flags or environment variables, creates the row with `invited_by` NULL, and prints a 24-hour sign-in link to standard output:

```
==================== ROOT LOGIN LINK ====================
member: root
valid:  24 hours
https://blog.example.com/l/xxxxxxxx
=========================================================
```

The markers make it findable in a container log stream. Once the table is non-empty, the seed values are ignored.

### 8.5 The social graph

Every member's inviter is displayed. This is a deliberate feature: in a closed community, knowing who vouched for whom is part of the trust structure. There is no way to hide it.

---

## 9. Posts

### 9.1 Structure

A post has a subject (one line, maximum 200 characters) and a body (plain text, maximum 16 KB). A reply has a body only (maximum 4 KB). There is no nesting; replies are a flat list under the post.

### 9.2 Ownership

The creating member owns the post. Ownership grants:

- The right to delete the post, which cascades to all its replies.
- The right to delete any reply within it.

A reply's author may delete their own reply. This is expressed in one statement:

```sql
DELETE FROM replies WHERE id = ? AND (
    user_id = ? OR post_id IN (SELECT id FROM posts WHERE user_id = ?)
)
```

Zero rows affected means the member had no right to delete it — indistinguishable from the row not existing, which is the correct behaviour.

### 9.3 The feed

`GET /feed` lists posts newest-first, 50 per page, with the subject, author handle, creation time, and reply count. The reply count is a correlated subquery, which is acceptable at this scale.

### 9.4 Editing

There is no edit function. The `updated_at` column exists in the schema for a future version but is currently always equal to `created_at`.

---

## 10. Chat

### 10.1 Structure

A channel is a post with `is_chat = 1`. Its name is the subject; its topic is the body and may be empty. Any member creates a channel by naming it — there is no approval step.

A chat message is a reply to that channel, maximum 2 KB.

### 10.2 The channel list

`GET /chat` lists channels sorted by `last_at` descending, so an active channel rises to the top. This differs from the blog feed, which sorts by creation time.

### 10.3 The retention limit

Each channel keeps the newest 500 messages (configurable). Older messages are deleted permanently and are not recoverable except from a volume snapshot.

The trim happens inside the same transaction as the insert:

```sql
-- 1. insert the message
INSERT INTO replies (post_id, user_id, body, created_at) VALUES (?, ?, ?, ?);

-- 2. move the channel to the top of the list
UPDATE posts SET last_at = ? WHERE id = ?;

-- 3. trim
DELETE FROM replies WHERE post_id = ? AND id <= (
    SELECT id FROM replies WHERE post_id = ?
    ORDER BY id DESC LIMIT 1 OFFSET ?
);
```

How the trim works: the inner `SELECT` with `OFFSET 500` returns the id of the 501st-newest message. The `DELETE` removes that row and every older one, leaving exactly 500. If the channel has fewer than 501 messages, the inner select returns no row, the comparison against NULL is never true, and nothing is deleted — the correct result with no separate count query.

The trim uses the row id rather than the timestamp, because two messages can share the same second and the id is always unique and ordered.

**Three consequences of this design:**

1. The limit is per channel, not per site. A member with 10 channels holds 10 × 500 messages.
2. Lowering the configured limit does not shrink a quiet channel; the trim only runs when a message arrives.
3. In the steady state, the trim costs one index seek and one row delete per message.

### 10.4 The channel page

`GET /c/{id}` renders the newest 100 messages, oldest first so the page reads top to bottom. There is no pagination backwards, because the retention limit already bounds the history.

Each message renders as one line:

```
09:22  geoff: Nice
09:23  geoff: Yeah
09:23  geoff: Word
```

This is a CSS grid with four columns — time, handle, text, delete control — so the columns align down the page and a wrapped message aligns under the text column rather than under the timestamp.

### 10.5 Live updates

New messages arrive without a page reload, via a poll:

```html
<div id="poller"
     hx-get="/c/{id}/lines?after={last_id}"
     hx-trigger="every 3s, newmsg from:body"
     hx-swap="outerHTML"></div>
```

The endpoint returns any messages with an id above `after`, followed by a replacement poller carrying the new highest id. Because the poller replaces itself with `outerHTML`, new messages land in the correct position and the poller returns to the end.

When the member sends a message, the POST returns `204 No Content` with the header `HX-Trigger: newmsg`. This fires the poller immediately, so the member's own message arrives through exactly the same path as everyone else's. **There is one render path and one insert path, so a duplicate message cannot appear.**

The query is:

```sql
SELECT ... FROM replies WHERE post_id = ? AND id > ? ORDER BY id ASC LIMIT ?
```

served by the existing `idx_replies_post` index. A poll that finds nothing costs one index seek.

**Load characteristics.** Each open page makes one request every 3 seconds. Twenty readers produce 400 requests per minute against a single database connection. Each is one indexed query usually returning nothing. If more than about 50 concurrent readers are expected, raise the interval — a member's own message still appears instantly via the trigger.

**Known behaviour:** the poll only appends. Messages removed by the trim remain visible to a reader who has the page open until they reload. This is accepted.

---

## 11. Geoblocking

### 11.1 Purpose and honesty about it

Geoblocking removes unwanted traffic from specified countries. **It is best-effort and is not a security control.** VPN services, proxies, and stale IP allocation data make the determination wrong for some clients. This should be stated to anyone who deploys the system.

### 11.2 Determining the country

Two sources, in priority order:

1. **A trusted proxy header.** If the peer address falls within a configured trusted-proxy prefix, the `CF-IPCountry` header is used. This header exists behind Cloudflare.
2. **A local range table.** A CSV file of `start_ip,end_ip,country_code` loaded at startup into a sorted array of `{start uint32, end uint32, code [2]byte}` and searched by binary search.

If neither is available, no request is blocked and the program logs a warning at startup.

The table covers IPv4 only. IPv6 clients are never blocked.

### 11.3 Client address

Behind a proxy, the real client address comes from the first entry of `X-Forwarded-For`, but **only if the peer is in the trusted-proxy list**. Otherwise the peer address is used. Without this check, any client could spoof their apparent location.

This address is also what appears on the keys page. If a proxy is present but not configured as trusted, every session shows the proxy's address.

### 11.4 Country codes

ISO 3166-1 alpha-2. The code for the United Kingdom is **GB**; `UK` is not an assigned code and blocks nothing. The configuration validator logs a warning if `UK` appears.

### 11.5 Placement

The geoblock is the outermost middleware, before the router and before any database access. A blocked request receives a plain-text 403 under 100 bytes and touches nothing else.

`/healthz` is exempt, because a platform health check originating from a blocked region would otherwise stop the container.

---

## 12. HTTP surface

### 12.1 Public routes

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | Health check; exempt from geoblock |
| GET | `/` | Sign-in form |
| POST | `/login` | Request a sign-in link |
| GET | `/l/{token}` | Consume the link, create a session |
| GET | `/terms` | Terms text |

### 12.2 Member routes

All require a valid session.

| Method | Path | Purpose |
|---|---|---|
| GET | `/feed` | Blog post list |
| GET | `/p/{id}` | One post with replies |
| POST | `/p` | Create a post |
| POST | `/p/{id}/delete` | Delete a post (owner only) |
| POST | `/p/{id}/r` | Add a reply |
| POST | `/r/{id}/delete` | Delete a reply |
| GET | `/chat` | Channel list |
| POST | `/chat` | Create a channel |
| GET | `/c/{id}` | One channel |
| GET | `/c/{id}/lines` | New messages fragment (HTMX) |
| POST | `/c/{id}/m` | Send a message |
| POST | `/c/{id}/delete` | Delete a channel (owner only) |
| GET | `/keys` | Session list |
| POST | `/keys/revoke-others` | Remove all other sessions |
| POST | `/logout` | Remove this session |
| GET | `/invite` | Invite form |
| POST | `/invite` | Create a member and send the link |
| GET | `/static/` | Style sheet and script |

### 12.3 Conventions

- Every state-changing route is POST and carries a CSRF token.
- Successful POSTs redirect (303 See Other), so a reload does not repeat the action. The exception is the chat message POST when called by HTMX, which returns 204.
- Requests with the header `HX-Request: true` receive fragments or empty bodies; the same routes without it receive redirects. **Every feature works with JavaScript disabled**, losing only the Enter-to-send shortcut and live updates.

---

## 13. Rendering

### 13.1 Template engine

Mustache, via `github.com/cbroglie/mustache`. Chosen for its logic-less design: templates cannot contain expressions, so display logic must live in Go.

All templates are embedded with `go:embed` and parsed once at startup into a map. Each file is also registered as a partial, so any template can include any other.

At startup the program verifies that every required template is present and refuses to start if one is missing, naming the file. Without this check, a missing template produces a 500 on every request — and on the chat poll, several times per minute.

### 13.2 Escaping — critical

**Every value originating from a member uses the double-brace form `{{value}}`, which escapes HTML. The triple-brace form `{{{value}}}` does not escape and appears nowhere in this project.**

Mustache escapes for HTML text context only. Therefore no member-supplied value may be placed in an `href`, `src`, inline style, or `<script>` block. The templates observe this.

### 13.3 Newlines

Post and reply bodies are multi-line. No template or Go code converts newlines. The style sheet uses `white-space: pre-wrap`, so line breaks render correctly with zero processing and zero injection risk.

### 13.4 Rendering procedure

Templates render into a buffer first. Only on success are the status code and headers written. This prevents a partially-rendered page being served with a 200 status.

### 13.5 Security headers

```
X-Content-Type-Options: nosniff
Referrer-Policy: same-origin
Content-Security-Policy: default-src 'none';
                         script-src 'self' https://cdn.jsdelivr.net;
                         connect-src 'self';
                         style-src 'self';
                         form-action 'self';
                         base-uri 'none'
```

Note `connect-src 'self'`: HTMX uses `XMLHttpRequest`, which is governed by `connect-src`, not `form-action`.

Note also that HTMX 2 injects an inline `<style>` element by default, which this policy blocks. It is disabled with:

```html
<meta name="htmx-config" content='{"includeIndicatorStyles":false}'>
```

No `unsafe-inline` and no `unsafe-eval` appear in the policy.

---

## 14. Client-side behaviour

### 14.1 HTMX

Loaded from a CDN with a subresource-integrity hash. It provides declarative AJAX through HTML attributes: `hx-get`, `hx-post`, `hx-trigger`, `hx-target`, `hx-swap`.

It is used in exactly three places: the chat message form, the chat poller, and chat message deletion. Everything else is plain HTML forms.

### 14.2 chat.js

About 40 lines, served from `/static/`, never inline. It does four things:

**Enter sends, Shift+Enter inserts a newline.**

```js
box.addEventListener("keydown", function (evt) {
    if (evt.key !== "Enter" || evt.shiftKey || evt.isComposing) { return; }
    evt.preventDefault();
    if (box.value.trim() !== "") { form.requestSubmit(); }
});
```

The `isComposing` check is required: without it, an East Asian input method sends the message when Enter is pressed to accept a candidate word.

`requestSubmit()` fires a real submit event, which HTMX intercepts — so the request goes through `hx-post`, not a page navigation.

**Auto-growing input**, capped at six lines.

**Clearing the box after a successful send**, via the `htmx:afterRequest` event.

**Scroll following.** After each swap, scroll to the newest message — but only if the reader was already at the bottom. Someone scrolled up reading history is not yanked away.

### 14.3 Chat layout

The chat page is a full-height flex column: a fixed header, a scrolling message pane, and an input box fixed at the bottom.

Two details make it behave like a chat client rather than a document:

**Messages sit at the bottom when there are few of them.** A block container stacks children from the top, so nine messages would sit at the top of a tall pane with empty space below. The fix is `margin-top: auto` on a wrapper inside the flex column: an auto margin absorbs free space, pushing content down when there is room and collapsing to zero when there is not.

`justify-content: flex-end` looks equivalent but is defective — with content taller than the pane, several browsers place the top of the content above the scroll origin where it cannot be reached.

**Message lines use a CSS grid**, not a hanging indent. An earlier version used `padding-left` with an equal negative `text-indent`; those are independent properties, and any later rule that changes one breaks the alignment silently. A grid declares the columns once and cannot come apart.

Units use `dvh` rather than `vh`, because `vh` is wrong on mobile browsers with a retracting address bar.

---

## 15. Security model

### 15.1 What is protected

| Asset | Protection |
|---|---|
| Member email addresses | Never rendered; only the handle is public |
| Session tokens | 32 random bytes, only the SHA-256 hash stored |
| Login tokens | Same, plus single-use and 15-minute expiry |
| Content | Every read requires a session |
| Membership list | Identical responses for known and unknown addresses |

### 15.2 CSRF

The token is derived, not stored:

```go
mac := hmac.New(sha256.New, app.secret)   // 32 random bytes, per process
mac.Write([]byte(sessionToken))
csrf := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
```

Every form carries it in a hidden field; every state-changing handler verifies it with `hmac.Equal`. Because it is derived from the session token, it needs no table and no server-side state, and it is automatically invalidated when the session ends.

The process secret is regenerated at each start, so a restart invalidates outstanding forms. Members see "the form is expired, try again" — acceptable for a system that restarts rarely.

`SameSite=Lax` on the cookie provides a second layer.

### 15.3 SQL injection

Every query uses parameter placeholders. The only string concatenation in a query is the sort direction in `ListPosts`, chosen from two local constants with no external input.

### 15.4 Input validation

All text passes through `CleanText`, which normalises line endings, strips control characters and invalid UTF-8, and trims whitespace. Single-line values additionally collapse runs of whitespace.

Length limits are enforced server-side. Request bodies are capped at 32 KB by `http.MaxBytesReader`.

Handles are restricted to letters, digits, `_`, `-`, and `.` — a small set, because handles appear on every page.

### 15.5 Mail header injection

Header values passed to the SMTP layer have carriage returns and newlines stripped and are truncated to 200 characters, so no input can inject an additional header line.

### 15.6 Logging

The request log rewrites `/l/<token>` to `/l/[token]`. No email address, token, or password appears in any log line. Error responses contain no internal error text; details go to the log only.

### 15.7 What is not protected

Stated plainly:

- **A sign-in link in an email is a bearer credential.** Anyone who reads the mailbox can sign in. This is inherent to the design; the keys page is the mitigation.
- **The geoblock is not a security control.**
- **There is no audit log.** Deletions leave no trace.
- **There is no rate limit on reads**, only on sign-in and content creation.
- **A member with root access to the host can read everything.** The database is not encrypted at rest by the application; disk-level encryption is the platform's responsibility.

---

## 16. Configuration

### 16.1 Sources, in precedence order

1. Built-in defaults
2. `config.json`, if present (a missing file is not an error)
3. Environment variables
4. The `PORT` variable, which overrides the listen address

### 16.2 Settings

| Key | Environment | Default | Meaning |
|---|---|---|---|
| `site_name` | `BLOG_SITE_NAME` | Blog | Name in header and mail subject |
| `site_url` | `BLOG_SITE_URL` | *required* | Base URL for sign-in links |
| `listen` | `BLOG_LISTEN` | 127.0.0.1:8080 | Listen address |
| `db_path` | `BLOG_DB_PATH` | blog.db | Database file |
| `terms` | `BLOG_TERMS` | empty | Terms page text |
| `footer` | `BLOG_FOOTER` | empty | Footer text |
| `blocked_countries` | `BLOG_BLOCKED` | GB,AU | Geoblock list |
| `trusted_proxies` | `BLOG_TRUSTED_PROXIES` | empty | CIDR prefixes |
| `geo_v4_file` | — | empty | IP range CSV path |
| `smtp_host` | `BLOG_SMTP_HOST` | localhost:25 | Mail relay |
| `smtp_user` | `BLOG_SMTP_USER` | empty | Relay username |
| `smtp_pass` | `BLOG_SMTP_PASS` | empty | Relay password |
| `mail_from` | `BLOG_MAIL_FROM` | *required* | Sender address |
| `invite_quota` | `BLOG_INVITE_QUOTA` | 5 | Open invites per member |
| `session_days` | — | 30 | Session lifetime |
| `posts_per_page` | — | 50 | Feed page size |
| `chat_keep` | `BLOG_CHAT_KEEP` | 500 | Messages kept per channel |
| `chat_per_page` | — | 100 | Messages shown per channel |

Unknown keys in `config.json` are an error, so a typo is reported rather than silently ignored.

### 16.3 Reloading

`SIGHUP` re-reads the configuration file and rebuilds the geo table. `listen` and `db_path` are not reloadable and retain their startup values. If a reload fails, the previous configuration stays active and the error is logged.

### 16.4 Mail

Port 25 is blocked outbound on essentially every cloud network, so a relay on port 587 with credentials is required in practice. The client uses STARTTLS when offered, and `smtp.PlainAuth` refuses to send credentials over an unencrypted connection — so the STARTTLS step must and does come first.

Mail is sent in a goroutine with a 10-second deadline so a slow relay never delays an HTTP response.

---

## 17. Source layout

Flat, in the repository root. This is appropriate for a single-binary program of this size; the Go team's own guidance permits it, and `internal/` plus `cmd/` would add path depth without benefit here.

| File | Lines | Contents |
|---|---|---|
| `app.go` | ~120 | `App` struct, domain types, context helpers |
| `config.go` | ~200 | Config struct, loading, validation, env overrides, reload |
| `db.go` | ~600 | Schema, migrations, every query |
| `token.go` | ~40 | Token generation and hashing |
| `auth.go` | ~230 | Sign-in flow, sessions, cookies, CSRF |
| `limit.go` | ~120 | Rate limiter, last-seen cache |
| `validate.go` | ~150 | Input cleaning and validation |
| `handlers.go` | ~700 | All HTTP handlers |
| `routes.go` | ~80 | Router, static files, request log |
| `views.go` | ~130 | Template loading and rendering |
| `geo.go` | ~200 | Country lookup, geoblock middleware |
| `mail.go` | ~120 | SMTP client |
| `main.go` | ~140 | Startup, signals, shutdown |
| `tmpl/*.mustache` | ~350 | 15 templates |
| `static/style.css` | ~250 | Style sheet |
| `static/chat.js` | ~40 | Chat keyboard and scroll |

Roughly 3,500 lines total including templates.

### 17.1 Naming convention

No identifier is shorter than three characters. Receivers are `app`, response writers `res`, requests `req`, users `usr`, transactions `txn`. The sole exception is the exported struct field `ID`, which follows Go's standard initialism convention.

### 17.2 Dependencies

Two, both pure Go:

- `modernc.org/sqlite` — SQLite driver, no cgo, so builds are static with no C toolchain
- `github.com/cbroglie/mustache` — templates

Everything else is the standard library. HTMX is loaded in the browser from a CDN and is not a build dependency.

---

## 18. Runtime behaviour

### 18.1 Startup

1. Load configuration; abort on error
2. Generate the 32-byte CSRF secret
3. Load the geo table
4. Open the database, creating the file if absent; run migrations
5. Load and verify templates
6. Seed the root member if the users table is empty
7. Start the hourly purge loop
8. Start the HTTP server

Any failure aborts with a message naming the cause.

### 18.2 Database connection

```
file:blog.db?_pragma=journal_mode(WAL)
            &_pragma=busy_timeout(5000)
            &_pragma=foreign_keys(1)
            &_pragma=synchronous(NORMAL)
```

- **WAL** allows concurrent readers during a write.
- **foreign_keys(1)** is required; SQLite disables them by default, and the cascade deletes depend on them.
- **synchronous(NORMAL)** does not flush the WAL on every commit. A clean shutdown or process crash loses nothing; a host power loss can lose the last few seconds. On a network-attached block volume this is the correct trade-off. Change to `FULL` if stronger durability is wanted; at this write rate the cost is negligible.

`SetMaxOpenConns(1)` is what makes `SQLITE_BUSY` impossible.

### 18.3 Signals

| Signal | Effect |
|---|---|
| SIGHUP | Reload config and geo table |
| SIGTERM, SIGINT | Graceful shutdown |

Shutdown: stop accepting connections, allow 20 seconds for in-flight requests, run `PRAGMA wal_checkpoint(TRUNCATE)` to fold the WAL back into the main file, close the database.

The platform's stop grace period must exceed 20 seconds. Many default to 5, which is too short.

### 18.4 Background work

- **Hourly:** delete expired sessions and login tokens older than a day.
- **Every 5 minutes:** clean the rate-limit map.
- **Every 30 minutes:** clean the last-seen cache.

### 18.5 Resource usage

Memory: a few megabytes, plus the geo table if loaded (about 2.5 MB for 250,000 IPv4 ranges). Binary: about 15 MB. Database: a few megabytes for a small community.

---

## 19. Deployment

### 19.1 Container image

Two-stage build. Stage one compiles with `CGO_ENABLED=0` and `-ldflags="-s -w"`. Stage two is `gcr.io/distroless/static-debian12:nonroot`, which supplies the CA bundle needed for STARTTLS and the timezone database, and contains no shell or package manager.

`/data` is the only writable path, because templates and assets are inside the binary.

Built for `linux/amd64` and `linux/arm64`; ARM instances are cheaper at most providers.

### 19.2 Runtime arrangement

Two containers via Docker Compose: blogchat, and Caddy for TLS.

The entire Caddy configuration:

```
blog.example.com {
    reverse_proxy blog:8080
}
```

Caddy obtains and renews the certificate from Let's Encrypt automatically and redirects port 80 to 443. No certificate management is required.

**Why not a cloud load balancer:** a managed load balancer costs more per month than the instance running the whole system. For a single container, terminating TLS on the instance is correct.

### 19.3 The storage constraint

This determines which platforms are viable:

| Storage type | Suitable | Reason |
|---|---|---|
| Block volume / disk | **Yes** | Correct POSIX locking |
| Instance local disk | Yes | Correct, but lost on instance rebuild |
| NFS / EFS | **No** | Advisory locks fail in ways SQLite cannot detect — corruption |
| Object storage via FUSE | **No** | No locking, no atomic rename |
| Ephemeral container FS | **No** | Data lost on every restart |

This rules out AWS App Runner, Google Cloud Run, and ECS on Fargate with EFS. The correct deployment target is a small virtual machine with an attached block volume.

### 19.4 Terraform

Configurations for Google Cloud Platform, Amazon Web Services, DigitalOcean, and Vultr. Each creates: one VM, one 10 GB data volume, one static address, one firewall (80, 443, and restricted SSH), and a daily snapshot schedule.

A shared cloud-init template does the same work on all four: locate the data disk, format it only if it has no filesystem, mount at `/data`, add an fstab entry, install Docker, write the compose file, start the containers.

Two details are essential:

**Docker must wait for the mount.** A systemd drop-in adds `RequiresMountsFor=/data`. Without it, Docker may start first, the container binds an unmounted directory on the boot disk, and a second empty database appears that vanishes at the next reboot.

**The format step must be conditional.** Formatting on every boot would destroy the database. The script checks for an existing filesystem and a volume label.

**The data volume has `prevent_destroy = true` with no override.** `terraform destroy` removes everything else and stops at the volume. Deleting it is a manual action in the provider console. This is the single most important safety property of the deployment.

### 19.5 Snapshots

One snapshot daily at 03:00 UTC, retained 28 days. GCP and AWS have native scheduled-snapshot resources; DigitalOcean and Vultr do not, so a cron job on the instance calls the provider API.

Snapshots are crash-consistent rather than clean. SQLite recovers from that state automatically. For a site with a few writes per minute, catching a write in progress is very unlikely, and adding checkpoint coordination was judged not worth the complexity.

On GCP the policy uses `KEEP_AUTO_SNAPSHOTS`, so snapshots survive even manual deletion of the disk.

**Restore:** create a disk from the snapshot, stop the instance, detach the current disk, attach the new one with the same device name, start. The mount script finds the `blogdata` label and proceeds normally.

---

## 20. Operations

### 20.1 First run

```bash
./blog -config config.json -seed-email you@example.com -seed-handle root
```

The root sign-in link is printed to standard output, valid 24 hours. Subsequent runs need no seed values.

### 20.2 Disabling a member

No admin interface exists. Use SQLite directly:

```sql
UPDATE users SET enabled = 0 WHERE handle = 'name';
```

Effective on the member's next request. **Do not delete the row** — the cascades destroy all their posts and messages.

### 20.3 Updating

Change the image tag and redeploy. The instance is replaced; the volume is not, so no data is lost. Always use a version tag; never `latest`, which would apply an untested version at the next restart.

### 20.4 Diagnosis

```bash
docker compose logs -f blog     # application
docker compose logs -f caddy    # certificate problems
curl localhost:8080/healthz     # bypasses proxy and firewall
ls -la /data/blog.db*           # three files: db, -wal, -shm
df -h /data                     # must be the data volume, not the boot disk
```

Layer-by-layer verification isolates faults: DNS, then instance reachability, then cloud-init completion, then mount, then containers, then the program, then the certificate.

---

## 21. Limits and constants

### Content

| Item | Limit |
|---|---|
| Subject / channel name | 200 characters |
| Channel topic | 200 characters |
| Post body | 16 KB |
| Reply body | 4 KB |
| Chat message | 2 KB |
| Handle | 2–24 characters |
| Email address | 254 characters |
| Request body | 32 KB |

### Time

| Item | Value |
|---|---|
| Sign-in link | 15 minutes, single use |
| Invitation link | 7 days, single use |
| Root bootstrap link | 24 hours, single use |
| Session | 30 days |
| Last-seen write interval | 60 seconds |
| Chat poll interval | 3 seconds |

### Rate limits (per member per minute unless noted)

| Action | Limit |
|---|---|
| Sign-in mails | 5 per 15 min, per address and per IP |
| Link opens | 20 per 15 min per IP |
| New posts | 10 |
| New replies | 20 |
| New channels | 5 |
| Chat messages | 60 |
| Open invitations | 5 total per member |

### Pagination

| Item | Value |
|---|---|
| Feed / channel list | 50 per page |
| Chat messages shown | 100 |
| Chat messages retained | 500 per channel |

---

## 22. Known limitations

Stated plainly so that nobody is surprised.

**Cannot scale horizontally.** SQLite with one connection means one instance, permanently. Scaling requires replacing the storage layer.

**No editing.** Posts, replies, and messages cannot be changed after submission.

**No search.** No full-text search over posts or messages.

**No notifications.** Members must visit to see new content. There is no email digest and no push.

**No file uploads.** Text only.

**No password recovery flow** — because there are no passwords. Access depends entirely on the email address remaining reachable.

**Handles are permanent.** No rename mechanism exists.

**No admin interface.** Member management requires direct database access.

**The inviter is always visible.** No way to hide the social graph.

**Chat history is lost by design.** Beyond 500 messages per channel, gone permanently except from volume snapshots.

**The geoblock is trivially bypassed** with any VPN.

**HTMX comes from a CDN.** Members behind restrictive networks lose live chat updates, and each page load contacts a third party. Self-hosting the file in `static/` removes this at the cost of the integrity guarantee the CDN hash provides.

**No IPv6 geoblocking.** The range table is IPv4 only.

**Deletion is unlogged.** No record of what was deleted or by whom.

**The Vultr Terraform configuration is untested** and has a known circular dependency in the snapshot script, plus an unverified assumption that Vultr supports block storage snapshots.

---

## 23. Glossary

**Channel** — A chat room. Internally a `posts` row with `is_chat = 1`.

**CSRF** — Cross-site request forgery. An attack where another site causes a member's browser to submit a request. Prevented here by a derived token in every form.

**Handle** — A member's public display name. Permanent.

**HTMX** — A browser library that adds AJAX behaviour through HTML attributes rather than written JavaScript.

**Key** — One session, one device. Members can view and revoke them.

**Mustache** — A logic-less template language. Templates can substitute and loop but cannot compute.

**Root member** — The first account, created at startup, with no inviter.

**Sign-in link** — A one-time URL that creates a session, sent by email.

**Trim** — The deletion of chat messages beyond the retention limit, performed inside the insert transaction.

**WAL** — Write-Ahead Log. A SQLite journal mode allowing readers to proceed during a write.

---

*End of specification.*

---

# blogchat — Technical Specification Appendices

**Supplement to Version 1.0**

These appendices hold reference material that supports the specification: complete signatures, decision records, failure catalogues, and verification procedures. Nothing here repeats the main document; each appendix cites the section it extends.

---

## Contents

- [A. Function reference](#appendix-a--function-reference)
- [B. Template context reference](#appendix-b--template-context-reference)
- [C. Decision record](#appendix-c--decision-record)
- [D. Rejected alternatives](#appendix-d--rejected-alternatives)
- [E. Failure catalogue](#appendix-e--failure-catalogue)
- [F. Query plans and index usage](#appendix-f--query-plans-and-index-usage)
- [G. State transitions](#appendix-g--state-transitions)
- [H. Verification matrix](#appendix-h--verification-matrix)
- [I. HTTP status codes in use](#appendix-i--http-status-codes-in-use)
- [J. Extension points](#appendix-j--extension-points)
- [K. Cost model](#appendix-k--cost-model)
- [L. Dependency and version matrix](#appendix-l--dependency-and-version-matrix)
- [M. Threat table](#appendix-m--threat-table)
- [N. Data lifecycle](#appendix-n--data-lifecycle)
- [O. Error message inventory](#appendix-o--error-message-inventory)

---

## Appendix A — Function reference

Extends specification §17. Every function that crosses a file boundary, with its file and purpose. Functions private to one file are omitted.

### A.1 Type definitions (`app.go`)

| Symbol | Kind | Notes |
|---|---|---|
| `App` | struct | Holds all process state; every field concurrency-safe |
| `User` | struct | `InviterHandle` is filled by a LEFT JOIN, empty for root |
| `Session` | struct | `Current` is set by the caller, not the query |
| `Post` | struct | Serves both blog posts and channels |
| `Reply` | struct | Serves both blog replies and chat messages |
| `FeedRow` | struct | Denormalised list row; avoids fetching bodies |
| `AuthHandler` | func type | `func(res, req, usr *User, raw string)` |
| `contextKey` | string type | Prevents collision with other packages' context keys |

The `raw` parameter on `AuthHandler` is the raw session token. It is passed because CSRF verification needs it. It deliberately does **not** carry the session row id, which is why three handlers call `SessionFrom` a second time — see D.7.

### A.2 Configuration (`config.go`)

| Function | Returns | Notes |
|---|---|---|
| `LoadConfig(path)` | `*Config, error` | Missing file is not an error; parse failure is |
| `(*Config) Validate()` | `error` | Also normalises: trims trailing slash from URL, upper-cases country codes |
| `(*Config) applyEnv()` | — | Runs after file, before validation |
| `(*App) Reload(path)` | `error` | Preserves `Listen` and `DBPath` from startup |
| `setStr`, `setInt`, `setList` | — | Empty string means "not set"; a genuine empty value cannot be set by env |

### A.3 Tokens (`token.go`)

| Function | Returns | Notes |
|---|---|---|
| `NewToken()` | `(raw string, sum []byte)` | Panics if `crypto/rand` fails |
| `HashToken(raw)` | `(sum []byte, valid bool)` | `valid` false on bad base64 or wrong length |

### A.4 Database (`db.go`)

**Lifecycle**

| Function | Notes |
|---|---|
| `OpenDB(path)` | Creates file if absent, applies migrations |
| `Migrate(dbh)` | Each migration in its own transaction |
| `Checkpoint(dbh)` | `wal_checkpoint(TRUNCATE)`; called at shutdown |

**Users**

| Function | Notes |
|---|---|
| `FindUserByEmail(email)` | Returns `ErrNotFound` rather than `sql.ErrNoRows` |
| `FindUserByID(uid)` | Currently unused; retained for future admin work |
| `CountUsers()` | Bootstrap test |
| `CreateUser(email, handle, inviter)` | `inviter = 0` means root |
| `CountOpenInvites(uid)` | Counts `last_login IS NULL` |

**Tokens and sessions**

| Function | Notes |
|---|---|
| `CreateLoginToken(uid, sum, life)` | |
| `ConsumeLoginToken(sum)` | Transactional; state check inside the UPDATE |
| `CreateSession(uid, sum, addr, agent, life)` | Truncates agent to 120 chars |
| `SessionByHash(sum)` | Joins users; enforces `enabled = 1` |
| `ListSessions(uid, current)` | Sets `Current` on the matching row |
| `DeleteOtherSessions(uid, keep)` | Returns count for the notice message |
| `DeleteSession(uid, sid)` | `uid` prevents cross-member deletion |
| `TouchSession(sid)` | Called only when `SeenCache` permits |
| `PurgeExpired()` | Hourly; tokens kept an extra day for diagnosis |

**Posts and replies**

| Function | Filters on `is_chat`? | Notes |
|---|---|---|
| `CreatePost(uid, subject, body, chat)` | sets it | Sets `last_at = now` |
| `ListPosts(chat, limit, offset)` | **yes** | Sort column depends on `chat` |
| `CountPosts(chat)` | **yes** | |
| `GetPost(pid)` | **no — deliberate** | Caller inspects `IsChat` to redirect |
| `DeletePost(pid, uid)` | no | Ownership is the only check needed |
| `CreateReply(pid, uid, body)` | no | Blog only; does not touch `last_at` |
| `ListReplies(pid)` | no | Blog only; unbounded |
| `ListChatLines(pid, limit)` | no | Newest N, returned oldest-first |
| `ListChatLinesAfter(pid, after, limit)` | no | Poll endpoint |
| `CreateChatLine(pid, uid, body, keep)` | no | Insert + `last_at` + trim, one transaction |
| `DeleteReply(rid, uid)` | no | Author or thread owner |

**Bootstrap**

| Function | Notes |
|---|---|
| `SeedFirstUser(email, handle)` | No-op if users exist; prints link with markers |

### A.5 Authentication (`auth.go`)

| Function | Notes |
|---|---|
| `SetSessionCookie(res, raw)` | `Secure` only when site URL is HTTPS |
| `ClearSessionCookie(res)` | `MaxAge: -1` |
| `SessionFrom(req)` | `(usr, sid, raw, found)` |
| `RequireAuth(hnd)` | Wraps; injects CSRF into context; updates last-seen after |
| `CSRFFor(raw)` | HMAC-SHA256 of session token under process secret |
| `CheckCSRF(req, raw)` | `hmac.Equal` |
| `StartLogin(email)` | **Returns nothing** — response must not reveal outcome |
| `FinishLogin(res, req, token)` | Consumes token, creates session, sets cookie |

### A.6 Validation (`validate.go`)

| Function | Notes |
|---|---|
| `CleanText(text)` | Preserves `\n` and `\t`; strips other control chars and `RuneError` |
| `CleanLine(text)` | `CleanText` then collapses whitespace runs |
| `ValidSubject(text)` | Non-empty, ≤200 runes |
| `ValidBody(text, limit)` | Non-empty, ≤limit **bytes** |
| `ValidTopic(text)` | **May be empty**, ≤200 runes |
| `ValidEmail(text)` | Minimal structural check only |
| `ValidHandle(text)` | 2–24 runes, restricted character set |

Note the inconsistency: subject and topic limits are counted in runes; body limits in bytes. This is intentional — display width matters for the one-line fields, storage size for the bodies.

### A.7 Rendering, geo, mail, routing

| Function | File | Notes |
|---|---|---|
| `LoadViews()` | views.go | Parses all; verifies required list; fails startup if any missing |
| `(*App) Base(req, usr)` | views.go | Reads CSRF from request context |
| `(*App) Render(res, req, name, ctx)` | views.go | Buffers first; sets security headers |
| `(*App) Fail(res, req, code, msg)` | views.go | Falls back to plain text if `error` template broken |
| `LoadGeo(conf)` | geo.go | Warns if block list set but no source available |
| `(*Geo) Lookup(addr)` | geo.go | IPv4 only; binary search |
| `(*Geo) ClientAddr(req)` | geo.go | XFF only from trusted peer |
| `(*Geo) Country(req)` | geo.go | Header beats table |
| `(*App) GeoBlock(next)` | geo.go | Exempts `/healthz` |
| `(*App) SendMail(to, subj, body)` | mail.go | STARTTLS then optional auth |
| `(*App) Routes()` | routes.go | Returns handler wrapped in request log |
| `isHTMX(req)` | handlers.go | Tests `HX-Request: true` |
| `chatItem(line, canDelete)` | handlers.go | Single source of chat line context |

---

## Appendix B — Template context reference

Extends specification §13. Every template and the keys it requires.

### B.1 Base keys — present in every context

| Key | Type | Source |
|---|---|---|
| `site_name` | string | Config |
| `footer` | string | Config |
| `terms` | string | Config |
| `handle` | string | Current user, empty if none |
| `authed` | bool | Whether a session exists |
| `csrf` | string | Request context, set by `RequireAuth` |

### B.2 Page templates

| Template | Additional keys |
|---|---|
| `login` | `sent`, `error` |
| `feed` | `posts[]`, `empty`, `chat`, `base`, `page`, `has_prev`, `prev_page`, `has_next`, `next_page`, `error` |
| `chats` | same as `feed`, plus `keep` |
| `post` | `post{}`, `replies[]`, `reply_count`, `owner`, `error` |
| `chat` | `post{}`, `channel_id`, `has_topic`, `lines[]`, `line_count`, `empty`, `owner`, `keep`, `last_id`, `error` |
| `lines` | `channel_id`, `lines[]`, `last_id` — **fragment, no base keys except `csrf`** |
| `keys` | `sessions[]`, `others`, `has_others`, `inviter`, `joined`, `notice` |
| `invite` | `sent`, `error`, `remaining`, `can_invite` |
| `terms` | none |
| `error` | `code`, `msg` |
| `blocked` | `country` — **unused; middleware sends plain text** |

### B.3 Partials

| Partial | Used by | Required keys |
|---|---|---|
| `header` | all pages | base keys |
| `footer` | all except `chat` | `footer` |
| `nav` | `post`, `terms` | none |
| `postform` | `feed` | `csrf` |
| `replyform` | `post` | `csrf`, `post.id` |
| `channelform` | `chats` | `csrf` |
| `messageform` | `chat` | `csrf`, `post.id` |
| `line` | `chat`, `lines` | `id`, `when`, `handle`, `body`, `can_delete`, `csrf`, `post.id` |

`chat` omits the footer partial deliberately: a footer inside a full-height flex layout consumes vertical space on every screen. The template closes `</body></html>` itself.

### B.4 Item shapes

**`posts[]`** — `id`, `subject`, `handle`, `when`, `replies`. `when` is creation time for blog, `last_at` for channels.

**`replies[]`** — `id`, `handle`, `body`, `when`, `can_delete`.

**`lines[]`** — same shape, but `when` is `HH:MM` only.

**`sessions[]`** — `id`, `when`, `last`, `ip`, `agent`, `current`.

**`post{}`** — `id`, `subject`, `body`, `handle`, `when`.

### B.5 Mustache parent-context resolution

`line.mustache` references `{{post.id}}` while iterating `{{#lines}}`. Line items have no `post` key, so Mustache walks up to the parent context and finds it there. This is specified behaviour, not an accident — but it means the `lines` fragment context must also supply `post.id`, or supply `channel_id` and have the template use that. The fragment uses `channel_id`.

---

## Appendix C — Decision record

Extends specification §2. Each entry: what was decided, why, and what it costs.

### C.1 Storage and structure

| # | Decision | Rationale | Cost |
|---|---|---|---|
| 1 | SQLite over Postgres | No server to operate; backup is one file | Cannot scale beyond one instance |
| 2 | One connection (`MaxOpenConns(1)`) | Eliminates `SQLITE_BUSY` entirely | All DB access serialised |
| 3 | `synchronous(NORMAL)` | Avoids fsync per commit | Host power loss can lose seconds |
| 4 | Shared `posts`/`replies` tables | One insert path, one ownership rule, one index set | Every post query must filter `is_chat` |
| 5 | `last_at` as a stored column | Index-scan sort for the channel list | Must be updated on every message |
| 6 | Trim by row id, not timestamp | Two messages can share a second | None |
| 7 | Trim inside the insert transaction | Count cannot drift | Slightly longer write transaction |
| 8 | Flat source layout | Appropriate at ~3,500 lines | Rejected `internal/`+`cmd/` — see D.1 |

### C.2 Authentication

| # | Decision | Rationale | Cost |
|---|---|---|---|
| 9 | No passwords | No credential store to protect | Access depends on mailbox reachability |
| 10 | Store token hashes only | DB leak yields no usable credentials | None |
| 11 | Identical response for unknown addresses | Prevents membership enumeration | User cannot tell if they mistyped |
| 12 | Redirect to `/keys` after sign-in | Makes unrecognised sessions visible immediately | One extra page in the flow |
| 13 | Derived CSRF token | No table, no server state, auto-invalidated | Restart expires open forms |
| 14 | Process secret regenerated at start | No secret to manage or rotate | Forms expire on restart |
| 15 | Invite creates the account immediately | No pending-invite table, no redeem step | Handle is chosen by inviter, not invitee |
| 16 | Quota counts `last_login IS NULL` | Self-clearing; no expiry job | Never-signing-in invitee holds a slot forever |

### C.3 Interface

| # | Decision | Rationale | Cost |
|---|---|---|---|
| 17 | Mustache over `html/template` | Logic-less; forces display logic into Go | Loses context-aware escaping — see M.3 |
| 18 | `white-space: pre-wrap` for newlines | Zero processing, zero injection risk | None |
| 19 | Buffer before writing response | No partial page with 200 status | Small memory cost per request |
| 20 | Startup check for required templates | Missing template would 500 on every poll | Must maintain the required list |
| 21 | HTMX over hand-written JS | ~40 lines of JS total | CDN dependency — see D.6 |
| 22 | Poll, not SSE | Compatible with one DB connection and 15s write timeout | 3s latency; steady request load |
| 23 | POST returns 204 + `HX-Trigger` | One render path; duplicates impossible | Message appears via poll, not response |
| 24 | CSS grid for chat lines | Columns declared once, cannot desynchronise | Replaced a defective hanging-indent — see E.9 |
| 25 | `margin-top: auto` for bottom-anchoring | Correct in all browsers | Requires a wrapper element — see D.8 |
| 26 | Both `method`/`action` and `hx-*` on forms | Works without JavaScript | Handlers branch on `HX-Request` |

### C.4 Deployment

| # | Decision | Rationale | Cost |
|---|---|---|---|
| 27 | Caddy on the instance, not a cloud LB | LB costs more than the whole system | TLS terminates on the instance |
| 28 | `prevent_destroy` with no override | The volume is the entire system's value | Teardown is deliberately incomplete |
| 29 | Snapshots only, no file backup | Crash-consistent is sufficient at this write rate | No provider-independent restore path |
| 30 | Fixed snapshot schedule (03:00, 28 days) | Two fewer variables to explain | Editing the module is required to change |
| 31 | Distroless image | No shell, no package manager | DB inspection needs a second container |
| 32 | ARM on AWS | Cheaper; image already multi-arch | Requires `arm64` in the image build |
| 33 | Public image, no registry auth | Removes credential handling from cloud-init | Image contents are public (they contain no secrets) |

---

## Appendix D — Rejected alternatives

Extends Appendix C. Options considered and declined, recorded so they are not re-litigated.

### D.1 `internal/` + `cmd/` source layout

Considered and implemented, then reverted. The standard layout requires `cmd/blog/main.go` plus `internal/blog/*.go`, moving templates and static assets under `internal/blog/` because `go:embed` cannot reference a parent directory.

**Rejected because:** at 3,500 lines with one binary, it adds two directory levels and an exported/unexported boundary that serves no consumer. The Go team's own position permits a flat layout for a small single-binary program; `golang-standards/project-layout` is not official.

### D.2 Postgres or MySQL

**Rejected because:** it introduces a server process, connection credentials, a backup procedure separate from the volume, and a network dependency — in exchange for horizontal scaling this system explicitly does not want.

### D.3 Server-Sent Events for chat

**Rejected because:** a long-lived connection per open page conflicts with the 15-second write timeout on the HTTP server and holds a goroutine per reader. The polling design costs one indexed query per reader per interval and has no such interaction.

### D.4 `<meta http-equiv="refresh">` for chat updates

**Rejected because:** it discards text in the input box on every refresh, making it unusable while typing.

### D.5 Rotate-180 chat pane (the Slack technique)

Rotating the scroll container 180° and each message back anchors content at the bottom by construction and prevents new messages moving the reading position.

**Rejected because:** it requires reversing the query order, breaks text selection across messages in some browsers, and inverts scroll-wheel direction unless separately corrected. `margin-top: auto` achieves the needed result with one CSS property.

### D.6 Self-hosting HTMX

The ~50 KB file could sit in `static/`, preserving the single-binary property and removing the third-party request on every page load.

**Not rejected — deferred.** The CDN was chosen with an integrity hash. Self-hosting remains a one-line change (`src="/static/htmx.min.js"`) plus removing `https://cdn.jsdelivr.net` from the CSP. Recommended for deployments where members may be behind restrictive networks.

### D.7 Passing the session id through `AuthHandler`

Three handlers (`ShowKeys`, `RevokeOtherKeys`, `Logout`) call `SessionFrom` a second time to obtain the session row id, because `AuthHandler` carries only the user and the raw token.

**Rejected because:** the signature was frozen before the handlers were written, and the second call is one indexed lookup on a local file. Changing it now would touch every handler for a saving measured in microseconds.

### D.8 `justify-content: flex-end` for bottom-anchored chat

**Rejected because it is defective:** when content exceeds the container height, several browsers place the top of the content above the scroll origin, where it cannot be reached. `margin-top: auto` has no such behaviour.

### D.9 Cloud Run, App Runner, Fargate+EFS

**Rejected because:** none provides storage with correct POSIX locking. App Runner has no persistent storage at all. Cloud Run offers GCS FUSE (no locking, no atomic rename) or NFS. Fargate offers EFS, which is NFS. Using any of them would corrupt the database — not fail loudly, corrupt.

### D.10 Cloud load balancer with managed certificate

**Rejected because:** a GCP or AWS load balancer costs roughly $16–18/month, more than the instance running the entire system. Caddy provides equivalent TLS termination for zero cost and two lines of configuration.

### D.11 `VACUUM INTO` file backup alongside snapshots

Considered: a daily clean copy on the volume, giving a provider-independent restore path.

**Rejected as over-engineering** for the stated write rate. A crash-consistent snapshot of a database with a few writes per minute is almost never mid-write, and SQLite recovers automatically. Retained as a recommendation for deployments that want provider-independent restore.

### D.12 SIGUSR1 checkpoint before snapshot

Considered: signal the process to run `wal_checkpoint` immediately before the snapshot fires, producing a clean rather than crash-consistent image.

**Rejected** for the same reason as D.11: it adds coordination between the snapshot scheduler and the process to remove a risk that is already negligible.

---

## Appendix E — Failure catalogue

Extends specification §20. Failures observed or predicted during development, with cause and resolution.

### E.1 Build and startup

| Symptom | Cause | Fix |
|---|---|---|
| `method App.X already declared` | Search/replace snippet applied twice; the anchor comment was consumed both times | Delete the duplicate block; verify with `grep -c "func (app \*App) X"` |
| `app.DeleteReply undefined` | Same cause — the duplicated insert consumed the function body, leaving only its comment | Restore the function from the spec |
| `template X.mustache is missing` at startup | File not created, or created after the binary was built | Create the file, rebuild; `go:embed` captures at build time |
| Startup aborts naming a config field | `site_url` or `mail_from` unset | Set it; both are mandatory |
| `unknown field` on config load | Typo in a JSON key | `DisallowUnknownFields` is deliberate |

### E.2 Templates and rendering

| Symptom | Cause | Fix |
|---|---|---|
| `render: template "lines" missing` repeating every 3s | Template file absent from the build; the chat poller retries indefinitely | Rebuild with the file present. The startup check (A.7) now prevents this class |
| Page renders but chat updates never appear | Poller element lost its attributes — `hx-swap` was not `outerHTML`, so it replaced its own *content* | Ensure `hx-swap="outerHTML"` on `#poller` |
| Navigation handle turns bold and blue | Unscoped `.who` rule from the chat block also matched the header's `<span class="who">` | Scope all chat rules under `.line` |

### E.3 CSS layout

| Symptom | Cause | Fix |
|---|---|---|
| Timestamps invisible; empty column on the left | `padding-left` applied but `text-indent` did not — the two properties came apart | Replace with CSS grid (C.24) |
| Wrapped message aligns under the timestamp | Same cause | Same fix |
| Nine messages sit at the top of an empty pane | Block container stacks from the top; `scrollTop = scrollHeight` is a no-op when content is shorter than the pane | `margin-top: auto` on a wrapper |
| Top of long chat history unreachable | `justify-content: flex-end` | Use `margin-top: auto` |
| Layout wrong on mobile with address bar | `vh` unit | Use `dvh` |
| Horizontal scrollbar from one long word | `1fr` grid column has content-based minimum | `minmax(0, 1fr)` |

### E.4 HTMX and CSP

| Symptom | Cause | Fix |
|---|---|---|
| Console reports blocked script | `default-src 'none'` with no `script-src` | Add `script-src 'self' https://cdn.jsdelivr.net` |
| HTMX loads but requests are blocked | `XMLHttpRequest` is governed by `connect-src`, not `form-action` | Add `connect-src 'self'` |
| Console reports blocked inline style | HTMX 2 injects indicator styles at load | `<meta name="htmx-config" content='{"includeIndicatorStyles":false}'>` |
| Enter sends mid-composition in Japanese/Chinese/Korean input | Missing `evt.isComposing` check | Add it |
| Message appears twice | POST rendered the line *and* the poll fetched it | POST must return 204 + `HX-Trigger` only |

### E.5 Deployment

| Symptom | Cause | Fix |
|---|---|---|
| cloud-init fails at image pull | Registry package still private | Make public; verify with `docker logout` then `docker pull` |
| Data gone after reboot | Docker started before `/data` mounted; container bound the boot disk | `RequiresMountsFor=/data` drop-in; check `df -h /data` |
| Volume not found on AWS | Nitro renamed `/dev/sdf` to `/dev/nvme1n1` | Mount script waits for the path then scans for an unformatted, unpartitioned disk |
| Certificate never issued | DNS wrong, port 80 closed, or Let's Encrypt rate limit from earlier failures | Verify with `dig` **before** first start; rate limit clears in a week |
| Every session shows the same IP | Proxy present but not in `trusted_proxies` | Set `BLOG_TRUSTED_PROXIES` |
| Geoblock blocks nobody | No range file and no trusted proxy | Startup logs a warning for exactly this |
| `UK` in block list has no effect | Not an assigned ISO code | Use `GB`; validator warns |
| No sign-in mail arrives | Port 25 blocked outbound | Use port 587 with credentials |
| `terraform destroy` fails at the volume | `prevent_destroy` | Working as designed; delete manually if truly intended |

---

## Appendix F — Query plans and index usage

Extends specification §5.6. Every query, its serving index, and its cost class.

| Operation | Query shape | Index used | Cost |
|---|---|---|---|
| Session lookup | `sessions.token_hash = ?` | UNIQUE on `token_hash` | 1 seek + 2 joins |
| Login token consume | `login_tokens.token_hash = ?` | UNIQUE on `token_hash` | 1 seek |
| Member by email | `users.email = ?` | UNIQUE on `email` | 1 seek |
| Blog feed page | `is_chat = 0 ORDER BY created_at DESC` | `idx_posts_kind_time` | 1 scan of N rows |
| Channel list page | `is_chat = 1 ORDER BY last_at DESC` | `idx_posts_kind_last` | 1 scan of N rows |
| Reply count per feed row | correlated `COUNT(*)` | `idx_replies_post` | 1 seek **per row** |
| Post by id | `posts.id = ?` | PRIMARY KEY | 1 seek |
| Thread replies | `post_id = ? ORDER BY id` | `idx_replies_post` | 1 range scan |
| Chat page load | `post_id = ? ORDER BY id DESC LIMIT 100` | `idx_replies_post` | 1 range scan, 100 rows |
| Chat poll | `post_id = ? AND id > ?` | `idx_replies_post` | 1 seek, usually 0 rows |
| Chat trim | `LIMIT 1 OFFSET 500` then `id <= ?` | `idx_replies_post` | 1 seek + 1 delete |
| Session list | `user_id = ?` | `idx_sessions_user` | 1 range scan |
| Revoke others | `user_id = ? AND id <> ?` | `idx_sessions_user` | 1 range scan |
| Open invite count | `invited_by = ? AND last_login IS NULL` | `idx_users_inviter` | 1 range scan |
| Purge expired | `expires_at <= ?` | **none — full scan** | Hourly, acceptable |

**Two notes.**

The reply-count subquery on the feed is O(page size) seeks. At 50 rows per page this is 50 index seeks — negligible on a local file, but it is the only query in the system whose cost grows with page size in a non-obvious way. If the feed ever felt slow, a stored `reply_count` column would be the fix, following the same reasoning as `last_at` (C.5).

`PurgeExpired` performs a full table scan on `sessions` and `login_tokens`. Adding an index on `expires_at` would help only if those tables grew large, which they do not: sessions are bounded by members × devices, and tokens are purged daily.

### F.1 Verifying a plan

```sql
EXPLAIN QUERY PLAN
SELECT pst.id FROM posts pst WHERE pst.is_chat = 1 ORDER BY pst.last_at DESC LIMIT 50;
```

Expect `USING INDEX idx_posts_kind_last`. If it reports `USE TEMP B-TREE FOR ORDER BY`, the index is not being used and the sort is happening in memory.

---

## Appendix G — State transitions

Extends specification §6–§10.

### G.1 Member lifecycle

```
   (does not exist)
         │
         │ POST /invite  or  bootstrap
         ▼
   ┌─────────────────┐
   │ INVITED         │  enabled=1, last_login=NULL
   │                 │  counts against inviter's quota
   └────────┬────────┘
            │ opens sign-in link
            ▼
   ┌─────────────────┐
   │ ACTIVE          │  enabled=1, last_login set
   │                 │  frees the inviter's quota slot
   └────────┬────────┘
            │ UPDATE users SET enabled=0
            ▼
   ┌─────────────────┐
   │ DISABLED        │  all sessions dead on next request
   │                 │  content remains visible
   └────────┬────────┘
            │ DELETE FROM users  (not recommended)
            ▼
   (gone — cascades destroy all posts, replies, sessions)
```

An invitation link expires after 7 days but the member row remains in INVITED state indefinitely, permanently consuming a quota slot. A new sign-in request from `/` issues a fresh 15-minute link, so the account remains usable.

### G.2 Login token

```
CREATED ──(15 min or 7 days)──▶ EXPIRED ──(hourly purge, +1 day)──▶ deleted
   │
   └──(GET /l/{token})──▶ USED ──(hourly purge, +1 day)──▶ deleted
```

The transition to USED is atomic: the state check lives inside the `UPDATE`, so two simultaneous requests cannot both succeed.

### G.3 Session

```
CREATED ──▶ ACTIVE ──(30 days)──▶ EXPIRED ──(hourly purge)──▶ deleted
              │
              ├──(POST /logout)──────────────▶ deleted
              ├──(revoke-others from another device)──▶ deleted
              └──(member disabled)──▶ row remains but fails the enabled check
```

The last case is important: disabling a member does not delete session rows. They remain until expiry but never authenticate, because `SessionByHash` requires `enabled = 1`.

### G.4 Chat message

```
INSERTED ──▶ VISIBLE ──▶ TRIMMED (permanent) when 500 newer messages exist
                │
                └──(author or channel owner deletes)──▶ gone
```

A subtlety: a reader with the page open keeps seeing trimmed messages until reload, because the poll only appends (§10.5).

---

## Appendix H — Verification matrix

Extends specification §20.4. Layer-by-layer tests; the first failure localises the fault.

### H.1 Deployment verification

| # | Layer | Command | Expected |
|---|---|---|---|
| 1 | DNS | `dig +short blog.example.com` | The instance address |
| 2 | Instance reachable | `curl -sI http://IP` | Any response |
| 3 | cloud-init | `sudo cloud-init status --wait` | `status: done` |
| 4 | Mount | `df -h /data` | 10 GB device, **not** the boot disk |
| 5 | fstab | `grep data /etc/fstab` | A UUID line with `nofail` |
| 6 | Docker ordering | `ls /etc/systemd/system/docker.service.d/` | `wait-for-data.conf` present |
| 7 | Containers | `docker compose ps` | Both `running` |
| 8 | Program | `curl localhost:8080/healthz` | `200` |
| 9 | Database | `ls -la /data/blog.db*` | Three files |
| 10 | TLS | `curl -sI https://domain/healthz` | `HTTP/2 200` |
| 11 | Root link | `docker compose logs blog \| grep -A3 'ROOT LOGIN'` | The link |
| 12 | Reboot persistence | reset instance, wait 90s, sign in | Content still present |
| 13 | Image update persistence | change tag, `terraform apply`, sign in | Content still present |
| 14 | Snapshot policy | provider-specific describe | Policy attached |

Step 12 is the single most important test: it validates the entire storage arrangement. Step 13 validates the update path, which is what every future change will exercise.

### H.2 Functional verification

| Area | Test | Expected |
|---|---|---|
| Sign-in | Unknown address | Same confirmation as a known one; no mail |
| Sign-in | Reuse a consumed link | "The link is used, or expired" |
| Sign-in | Link after 15 minutes | Same message |
| Keys | Sign in on a second device | Two rows; current one marked |
| Keys | Revoke others | Second device redirected to sign-in |
| Invite | Duplicate handle | "That address or that handle is not available" |
| Invite | Sixth open invite | Quota message |
| Posts | Delete another member's post | 403 |
| Posts | Delete a reply in your own thread | Succeeds |
| Chat | `/p/{id}` on a channel | Redirects to `/c/{id}` |
| Chat | `/c/{id}` on a post | Redirects to `/p/{id}` |
| Chat | Shift+Enter | Newline; box grows; no send |
| Chat | Enter | Sends; box clears; keeps focus |
| Chat | Second window | Message appears within 3 seconds |
| Chat | Scroll up, receive message | Position unchanged |
| Chat | Exceed the keep limit | Oldest deleted; count stays at limit |
| CSP | Browser console | No blocked resources |
| No-JS | Disable JavaScript, send a message | Works via redirect |
| Geoblock | Request from a blocked country | 403, plain text |
| Geoblock | `/healthz` from a blocked country | 200 |

---

## Appendix I — HTTP status codes in use

Extends specification §12.3.

| Code | Meaning here | Emitted by |
|---|---|---|
| 200 | Page rendered; or HTMX delete succeeded (empty body) | `Render`, delete handlers |
| 204 | Chat message accepted; poller will fetch it | `CreateChatLineHandler` under HTMX |
| 303 | Post-Redirect-Get after any state change | All non-HTMX POST handlers |
| 400 | Malformed request, or wrong kind (post vs channel) | `parseForm` failure, kind guards |
| 403 | Geoblocked; failed CSRF; not the owner | `GeoBlock`, `CheckCSRF`, ownership checks |
| 404 | Post, channel, or reply does not exist | Id parse failure, `ErrNotFound` |
| 413 | Request body over 32 KB | `http.MaxBytesReader` |
| 429 | Rate limit on sign-in link opens | `ConsumeLink` |
| 500 | Template or database failure | `Render`, `Fail` |
| 503 | Health check with database unreachable | `/healthz` |

**Note:** 403 covers three distinct conditions deliberately. Ownership failures return 403 rather than 404 because the resource genuinely exists; the member simply has no right to it. Non-existent resources return 404. The `DeleteReply` query, however, cannot distinguish "not yours" from "does not exist" — both yield zero affected rows — and returns 403. This is the correct conservative choice.

---

## Appendix J — Extension points

Where a future change would go, and what it would disturb. Extends specification §22.

| Feature | Where | Difficulty | Disturbs |
|---|---|---|---|
| Edit posts | New route + handler; `updated_at` already exists | Low | Nothing; the column is reserved |
| Full-text search | SQLite FTS5 virtual table + trigger | Medium | New migration; driver must include FTS5 |
| Email digest | New goroutine in the hourly loop | Medium | Needs a `last_notified` column |
| Handle rename | Handler + uniqueness check | Low | Breaks the "permanent handle" property |
| Admin interface | New `is_admin` column + routes | Medium | New authorisation layer throughout |
| Audit log | New table + calls in delete handlers | Low | Storage growth |
| Per-site chat quota | Count channels per member in `CreateChannelHandler` | Low | Nothing |
| Global trim pass | Add to `purgeLoop` | Low | Makes `chat_keep` reductions retroactive |
| IPv6 geoblocking | Second sorted array of `uint128` ranges | Medium | Parallel code path in `geo.go` |
| Self-hosted HTMX | Move file to `static/`; edit CSP and script src | Trivial | Loses SRI guarantee |
| Multiple instances | Replace `db.go` with a network database | **High** | Invalidates C.1, C.2, and §19.3 entirely |
| Read receipts | New table keyed on member+channel | Medium | Write amplification on the single connection |

The last row is the boundary of the design. Everything above it is an addition; that one is a different system.

---

## Appendix K — Cost model

Extends specification §19. Approximate monthly figures; verify current prices.

### K.1 Infrastructure

| Component | GCP | AWS | DigitalOcean | Vultr |
|---|---|---|---|---|
| Instance | $0 (free tier) – $7 | ~$12 (t4g.small) | $6 | $6 |
| Data volume 10 GB | ~$1 | ~$1 | $1 | $1 |
| Static address | included | included | included | included |
| Snapshots (28 daily) | <$0.10 | <$0.10 | ~$0.05/GB | ~$0.05/GB |
| Egress (text-only) | negligible | negligible | included | included |
| **Total** | **$1–8** | **~$13** | **~$7** | **~$7** |

A cloud load balancer, had one been used, would add $16–18 — more than doubling the cost of the entire system. This is why C.27 chose Caddy.

### K.2 Storage growth

| Item | Approximate size |
|---|---|
| Member row | ~200 bytes |
| Session row | ~250 bytes |
| Post (16 KB body) | up to 16 KB |
| Typical post | ~1 KB |
| Chat message | ~150 bytes |
| Full channel (500 messages) | ~75 KB |
| 50 members, 1,000 posts, 20 channels | ~3 MB |

The 10 GB volume is sized for the filesystem and snapshot mechanics, not for the data. A community of this kind will not approach it.

### K.3 Snapshot cost behaviour

The first snapshot bills for used blocks (~1 GB including filesystem overhead). Subsequent snapshots bill only for changed blocks — a few megabytes per day. Twenty-eight retained snapshots therefore cost close to the first one plus a small increment, not 28× the volume.

---

## Appendix L — Dependency and version matrix

Extends specification §17.2.

### L.1 Build-time

| Item | Version | Why this one |
|---|---|---|
| Go | 1.24 | `ServeMux` method patterns (1.22+); `crypto/rand` no longer returns an error (1.24) |
| `modernc.org/sqlite` | ~1.34 | Pure Go — enables `CGO_ENABLED=0` and a static binary |
| `github.com/cbroglie/mustache` | ~1.4 | Supports `StaticProvider`, `ParseStringPartials`, `FRender`, and dotted names |

**Verify three names on first build:** `StaticProvider`, `ParseStringPartials`, and `Template.FRender`. Mustache packages differ on these. If they differ, only `views.go` changes.

**Verify dotted-name support:** templates use `{{post.subject}}` and `{{post.id}}`. If unsupported, flatten the `post` map in `ShowPost`/`ShowChannel` to `post_subject`, `post_id`, and adjust `post.mustache`, `chat.mustache`, `replyform.mustache`, `messageform.mustache`, `line.mustache`.

### L.2 Runtime

| Item | Version | Notes |
|---|---|---|
| HTMX | 2.0.10 | CDN with SRI hash; only in the browser |
| Caddy | 2-alpine | Separate container |
| Base image | distroless static-debian12:nonroot | Supplies CA bundle and tzdata |

### L.3 Deployment tooling

| Item | Version |
|---|---|
| Terraform | ≥1.5 |
| `hashicorp/google` | ~6.0 |
| `hashicorp/aws` | ~5.0 |
| `digitalocean/digitalocean` | ~2.0 |
| `vultr/vultr` | ~2.0 |

### L.4 What is deliberately absent

No web framework, no ORM, no router library, no logging library, no configuration library, no test framework beyond the standard one, no CSS framework, no JavaScript build step, no bundler, no transpiler.

---

## Appendix M — Threat table

Extends specification §15.

| # | Threat | Mitigation | Residual risk |
|---|---|---|---|
| 1 | Password database theft | No passwords exist | — |
| 2 | Session token theft from database | Only SHA-256 hashes stored | — |
| 3 | Session theft via script | `HttpOnly` cookie; strict CSP; no `unsafe-inline` | XSS via a CSP gap |
| 4 | Cross-site request forgery | HMAC token in every form + `SameSite=Lax` | — |
| 5 | SQL injection | Parameters everywhere; one concatenation from local constants only | — |
| 6 | HTML injection | Mustache double-brace escaping; triple-brace banned | See M.3 below |
| 7 | Membership enumeration via sign-in | Identical response for unknown addresses | — |
| 8 | Membership enumeration via invite | Identical message for duplicate email and duplicate handle | — |
| 9 | Mail flooding | 5 per 15 min per address and per IP | — |
| 10 | Mail header injection | CR/LF stripped, values truncated | — |
| 11 | Brute-force of a sign-in link | 256-bit token, 15-minute window, single use | — |
| 12 | Content spam | Per-member rate limits on every create action | A determined member can still fill the DB |
| 13 | **Intercepted sign-in link** | Keys page + revoke-others | **Unmitigated at the protocol level** |
| 14 | **Compromised mailbox** | Same | **Unmitigated by design** |
| 15 | **Geographic restriction bypass** | None | **Trivially bypassed by VPN** |
| 16 | **Host compromise** | Distroless image; non-root user | **Root on host reads everything** |
| 17 | **Malicious member** | Ownership rules limit deletion scope | **No audit trail; deletion is silent** |
| 18 | CDN compromise | Subresource integrity hash | Availability dependency remains |
| 19 | Denial of service | Rate limits on writes only | No read-side rate limit |

### M.1 Bearer-credential property

Threats 13 and 14 are the fundamental limit of passwordless email authentication: anyone who reads the mailbox can sign in. This is inherent, not a defect in the implementation. The keys page exists specifically as the detection and recovery mechanism, which is why sign-in redirects there (C.12).

### M.2 What "not a security control" means for the geoblock

The geoblock reduces unwanted traffic volume. It must not be relied upon for legal compliance, access control, or data-residency claims. Any member with a VPN is unaffected, and IPv6 clients are never blocked at all.

### M.3 The Mustache escaping boundary

Mustache escapes for **HTML text context only**. It does not escape for attribute, URL, CSS, or script context. Therefore:

- Member-supplied values may appear as element text. ✓
- Member-supplied values must **never** appear in `href`, `src`, `style`, `on*` attributes, or `<script>` blocks. ✗

`html/template` would enforce this automatically by context; Mustache does not. This is the cost of decision C.17 and the reason it is stated three times across the documentation.

---

## Appendix N — Data lifecycle

Extends specification §5 and Appendix G.

| Data | Created | Retained | Deleted by | Recoverable? |
|---|---|---|---|---|
| Member row | Invite or bootstrap | Indefinitely | Manual SQL only | From snapshot |
| Email address | With the member | Life of the member | With the member | From snapshot |
| Login token | Sign-in request | 15 min or 7 days | Hourly purge, +1 day | No (and no value) |
| Session | Link consumption | 30 days | Purge, logout, revoke, expiry | No |
| Session IP/agent | With the session | With the session | With the session | No |
| Post | Member action | Indefinitely | Owner, or member cascade | From snapshot |
| Blog reply | Member action | Indefinitely | Author, thread owner, or cascade | From snapshot |
| Channel | Member action | Indefinitely | Owner, or member cascade | From snapshot |
| **Chat message** | Member action | **500 newest per channel** | **Trim, author, owner, cascade** | **From snapshot only** |
| Request log | Every request | Platform retention | Platform | Platform |
| Snapshot | Daily 03:00 UTC | 28 days | Retention policy | — |

### N.1 Cascade consequences

Deleting a `users` row cascades to `login_tokens`, `sessions`, `posts`, and `replies` — and posts cascade further to their replies. **A single member deletion can therefore remove content written by other members** (their replies inside the deleted member's threads).

This is why §20.2 states that `enabled = 0` is the correct removal mechanism and deletion is not.

### N.2 The only permanent loss

Chat trimming is the one place where the system destroys data during normal operation, with no confirmation and no undo. Both the channel list and the channel page state the retention limit for this reason.

---

## Appendix O — Error message inventory

Every message a member can see, its trigger, and its status code. Extends Appendices I and E. Messages are deliberately vague where vagueness prevents information disclosure.

| Message | Trigger | Code | Deliberately vague? |
|---|---|---|---|
| "The email address is not valid" | Failed structural check | 200 (re-render) | No |
| "Too many requests, wait 15 minutes" | Sign-in rate limit | 200 (re-render) | No |
| "If that address belongs to a member, a sign-in link is now on the way." | Any sign-in submission | 200 | **Yes** — hides membership |
| "The link is used, or expired. Request a new link." | Bad, used, or expired token; or disabled member | 200 | **Yes** — does not distinguish |
| "The form is expired, try again" | CSRF mismatch | 403 | No |
| "The subject is empty" / "is too long" | Validation | 303 + query | No |
| "The message is empty" / "is too long" | Validation | 303 + query | No |
| "The handle must have 2 to 24 characters" | Validation | 200 | No |
| "The handle accepts letters, digits, and the signs _ - ." | Validation | 200 | No |
| "That address or that handle is not available" | UNIQUE violation on either column | 200 | **Yes** — does not distinguish |
| "Your open invites are at the limit" | Quota reached | 200 | No |
| "The post does not exist" | Bad id or `ErrNotFound` | 404 | No |
| "The channel does not exist" | Bad id or `ErrNotFound` | 404 | No |
| "You do not own this post" | Zero rows on delete | 403 | No |
| "You cannot delete this reply" | Zero rows on delete | 403 | Partly — also covers non-existence |
| "That is a channel, not a post" | Kind guard on reply POST | 400 | No |
| "That is a post, not a channel" | Kind guard on message POST | 400 | No |
| "Too many posts / replies / channels / messages, wait one minute" | Per-action rate limit | 303 + query | No |
| "Not available in your region." | Geoblock | 403 | No |
| "The request is too large" | Body over limit | 400 | No |
| "Server error" | Template or database failure | 500 | **Yes** — details go to log only |

### O.1 Principle

Two rules govern this inventory:

1. **A message shown to an unauthenticated visitor must not reveal whether an account exists.** This governs the sign-in confirmation and the invalid-link message.
2. **A message shown after an internal failure must contain no internal detail.** The response says "server error"; the log carries the cause and the request path.

---

*End of appendices.*

