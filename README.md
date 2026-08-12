# FFS: Federated Forum System

One Go process and one SQLite file. Text only. Invite only. No passwords, email auth only.

Two areas share the same backend:

- **Posts.** A blog thread with a subject and a body. The list sorts on the creation time. All replies stay.
- **Chat.** A channel with a name and an optional topic. The list sorts on the time of the newest message. Each channel keeps the newest messages only and removes the rest.

A channel is a post row with the `is_chat` column set to 1, and a chat message is a reply to that row. There is no separate table.

### Posts

<img src="https://raw.githubusercontent.com/ghowland/blogchat/refs/heads/main/docs/posts.png" width=50% height=50%>

Post and reply with URL markup.  No images.

### Chat

<img src="https://raw.githubusercontent.com/ghowland/blogchat/refs/heads/main/docs/chat.png" width=50% height=50%>

## AI Usage Disclosure

This README was written by Opus 5.0, and all of the code was as well.  And the Terraform configs.  Hand editing will take place as usage testing finds issues, so it remains stable after the initial features are stable.

### Federation Algorithm

<img src="https://raw.githubusercontent.com/ghowland/blogchat/refs/heads/main/docs/ffs_flow.png" width=50% height=50%>

#### Update

```
TARGET                          ORIGIN
    |                               |
    |  give me items after 4192     |
    |------------------------------>|
    |     signed with our key       |
    |                               |
    |     items 4193..4207          |
    |<------------------------------|
    |     high=4207  more=no        |
    |                               |
  for each item:
    wanted topic?  no -> skip
    valid?         no -> skip
    seen hash?     yes -> skip
    else store
    cursor = item seq          (skips move it too)

  if relay on: publish it again
               new seq, remote = true
```

#### Authoring and Replying

```
ALICE writes on server A
    |
    v
  A: seq 4193, remote=false        <- A's members read it here
    |
    |  B pulls from A
    v
  B: seq 118, remote=true          <- B's members read it here
    |
    |  B has relay on, so it publishes it again
    |
    +---- C pulls ----> C: seq 62, remote=true
    |                     |
    |                     +---- E pulls ----> E: seq 9004, remote=true
    |
    +---- D pulls ----> D: seq 771, remote=true
                          |
                          +---- C pulls ----> already have this hash, skip


  Every server got it by asking, not by being sent it.
  Every hop is just a normal post on that server.
  Only C and D know B had it.  Nobody past B knows about A.

  BOB on D replies
    |
    |  his reply quotes Alice's post in the body
    v
  D: seq 772, remote=false
    |
    +---- B pulls ----> B works out the parent from the quote,
                        finds its own copy, hangs the reply on it
                        |
                        +---- A pulls ----> A does the same,
                                            Alice sees the reply
```

## Build

The Go source, the templates, and the client script are in `code/`.

    cd code
    go mod tidy
    go build -o ffs .

The binary is static. The SQLite driver is pure Go, so the build needs no C toolchain.

Two notes for a first build:

- The first compile of the SQLite driver takes several minutes and prints nothing. Later builds use the cache and are fast.
- The templates and the client script are compiled into the binary. An edit to a file in `code/template/` or `code/static/` has no effect until you build again and restart.

## First run

Copy `config.json` and set `site_url` and `mail_from`. The database file does not need to exist; the program creates the file and the schema.

The platform has no registration page, so the first member cannot invite himself. Give the address and the handle of the first member one time:

    ./ffs -config ../config.json -seed-email you@example.com -seed-handle root

The program prints a sign-in link to standard output. The link is valid for 24 hours. Later runs need no seed flags.

## Configuration

`config.json` holds the settings. Every key is optional except `site_url` and `mail_from`. An environment variable overrides a file value, which is how a container deployment sets the values.

| Key | Environment | Default | Function |
|---|---|---|---|
| `site_name` | `BLOG_SITE_NAME` | Blog | The name in the header |
| `site_url` | `BLOG_SITE_URL` | none | The address the sign-in link uses |
| `listen` | `BLOG_LISTEN` | 127.0.0.1:8080 | The listen address, at startup only |
| `db_path` | `BLOG_DB_PATH` | blog.db | The database file, at startup only |
| `terms` | `BLOG_TERMS` | empty | The text of the terms page |
| `footer` | `BLOG_FOOTER` | empty | The text in the footer |
| `blocked_countries` | `BLOG_BLOCKED` | GB, AU | The geoblock list |
| `trusted_proxies` | `BLOG_TRUSTED_PROXIES` | empty | Prefixes that can set the forward headers |
| `geo_v4_file` | — | empty | The path of the country table |
| `smtp_host` | `BLOG_SMTP_HOST` | localhost:25 | The mail relay |
| `smtp_user` | `BLOG_SMTP_USER` | empty | The relay user name |
| `smtp_pass` | `BLOG_SMTP_PASS` | empty | The relay password |
| `mail_from` | `BLOG_MAIL_FROM` | none | The sender address |
| `invite_quota` | `BLOG_INVITE_QUOTA` | 5 | Open invitations for each member |
| `session_days` | — | 30 | The life of a key |
| `posts_per_page` | — | 50 | Rows on a list page |
| `chat_keep` | `BLOG_CHAT_KEEP` | 500 | Messages kept in each channel |
| `chat_per_page` | — | 100 | Messages shown on a channel page |

Outbound port 25 is closed on nearly every network of a cloud provider, so a relay on port 587 with a user name and a password is necessary in practice. Without working mail, nobody can sign in.

## Operation

| Signal | Result |
|---|---|
| SIGHUP | Reload the configuration file and the country table |
| SIGTERM, SIGINT | Stop, checkpoint the write-ahead log, close the database |

The listen address and the database path apply at startup only. A change of those two values needs a restart.

To disable a member, set the `enabled` column to 0:

    sqlite3 blog.db "UPDATE users SET enabled = 0 WHERE handle = 'name'"

The member loses access on the next request, because the session query reads the column.

Do not delete the row. The foreign keys cascade, so a delete removes every post and message of that member, and also the replies that other members wrote inside those threads.

## Sign-in and keys

There are no passwords. A member gives an email address on the start page and receives a link. The link makes a session on the device that opens it.

Each device that signs in holds one key. The `/keys` page lists every key of the member with the sign-in time, the last-seen time, the address, and the device text. One button removes every key except the key of the current device, so the other devices need a new link.

**A sign-in link works on any device that opens it.** A person who reads the mailbox of a member can sign in as that member. The keys page is how a member sees this and removes the access.

A sign-in opens the keys page, so that a member sees the active keys at every sign-in.

## Invitations

There is no registration page. A member gives an email address and a handle, and the account exists at once. The new member receives a link that is valid for 7 days.

The handle is the public name. The email address is private and no page shows it. Every member sees who invited whom; the first member shows as the founder.

A member holds at most 5 open invitations. An invitation is open until the person completes the first sign-in.

## Posts

A post has a subject and a body. A reply has a body only. There is no nesting; the replies are one flat list under the post.

The member who writes a post owns it. The owner can delete the post, which removes every reply in it, and can delete any reply in it. A member can always delete his own reply.

There is no edit function.

## Chat

Any member makes a channel from the `/chat` page. The channel needs a name.  The topic line is optional and appears under the name on the channel page.  The member that makes the channel owns it and can delete it. A delete of a channel removes every message in it.

Each channel keeps the newest `chat_keep` messages. When a new message arrives, the program adds the message, moves the channel to the top of the list, and removes the messages above the limit. The three operations are in one transaction. **Removed messages are gone and no backup of them exists inside the program.**

The channel page shows the newest `chat_per_page` messages, oldest first, so that the page reads from top to bottom. There are no older pages, because the keep limit already removes the old messages.

A member can delete his own message. The owner of the channel can delete any message in that channel.

Three properties of the limit need attention:

1. The limit applies to each channel and not to the site. A member with 10 channels holds 10 times `chat_keep` messages.
2. A reduction of `chat_keep` does not shorten a quiet channel, because the trim runs only when a new message arrives.
3. A `/p/{id}` address for a channel redirects to `/c/{id}`, and a `/c/{id}` address for a blog post redirects to `/p/{id}`. A wrong link corrects itself.

## URLs in text

The program finds web addresses in the body of a post, the body of a reply, and a chat message, and makes them into links. The subject of a post, the name of a channel, and the topic line are not changed.

There is no markup language. You write a plain address and the program finds it. There is no method to make a link with different text, and there is no method to write bold text, a list, or a heading.

Rules of the scan:

| Item | Behaviour |
|---|---|
| `http://` and `https://` | Become links |
| Every other scheme | Stays as plain text |
| An address without a scheme, such as `example.com` | Stays as plain text |
| A full stop or a comma at the end | Stays outside the link |
| An address longer than 60 characters | The page shows a shorter form; the link goes to the full address |

The program makes a link only for the two web schemes, so no other scheme can reach the page. Text that a member writes stays escaped in every other case.

Links open in a new tab.

## Interface

The pages are complete HTML from the server. Two files come from a content network at page load, each with an integrity hash:

| File | Function | If it does not load |
|---|---|---|
| Tailwind | All styling | The pages work and have no style |
| HTMX | Chat updates and the send key | The chat works with a page reload for each message |

Neither file is necessary for correctness. The site works with JavaScript turned off; only the chat updates and the Enter key are lost.

In the chat, Enter sends the message and Shift with Enter makes a new line. The page asks the server for new messages every three seconds. Your own message appears at once, because the send makes the same request fire.

The pages follow the light or dark setting of the operating system. There is no setting inside the site.

## Geoblocking

The block list is in `blocked_countries`. The default list is `GB` and `AU`.  Use ISO 3166-1 alpha-2 codes. The code for the United Kingdom is `GB`. The code `UK` is not assigned and blocks nothing.

There are two sources for the country of a request:

1. The `CF-IPCountry` header. The program reads the header only when the peer address is inside a prefix in `trusted_proxies`.
2. A local table. Set `geo_v4_file` to a CSV file with three columns: the first address, the last address, and the two-letter code. The address columns accept the dotted form and the decimal form.

With no proxy and no table, no request is blocked and the program writes a warning at startup.

The table holds IPv4 ranges only, so a client with an IPv6 address is never blocked.

**This filter is best effort.** VPN services and old allocation data make the country wrong for some clients. The filter removes unwanted traffic. It is not a security control and it is not a legal control.

## Limits

| Item | Value |
|---|---|
| Subject, channel name | 200 characters |
| Channel topic | 200 characters |
| Post body | 16 KB |
| Reply body | 4 KB |
| Chat message | 2 KB |
| Handle | 2 to 24 characters |
| Request body | 32 KB |
| Link text on the page | 60 characters |
| Sign-in link | 15 minutes, one use |
| Invitation link | 7 days, one use |
| Session | 30 days |
| Sign-in mails | 5 in 15 minutes for each address and each client address |
| Open invitations | 5 for each member |
| New posts | 10 each minute for each member |
| New replies | 20 each minute for each member |
| New channels | 5 each minute for each member |
| New chat messages | 60 each minute for each member |
| Messages kept | 500 for each channel, from `chat_keep` |
| Messages on a page | 100, from `chat_per_page` |

## Deployment

The `terraform/` directory holds a configuration for Google Cloud Platform, Amazon Web Services, DigitalOcean, and Vultr. Each one makes one virtual machine, one disk for the database, and two containers: this program, and Caddy, which gets a certificate and sends port 443 to port 8080.

Three properties are necessary on any host:

1. **One instance only.** The program uses SQLite with one connection. Two instances give two databases, or a damaged one.
2. **A block disk for the database.** A network file system such as NFS, and a mount of object storage, do not give the file locks that SQLite needs. The result is damage and not an error message.
3. **A mail relay on port 587 with a user name and a password.**

See `terraform/README.md`.

## Backup

Stop the process and copy `blog.db`, or use the online command on a running database:

    sqlite3 blog.db ".backup backup.db"

The whole database is three files while the program runs: `blog.db`, `blog.db-wal`, and `blog.db-shm`. A copy of the first file alone, taken while the program runs, is not complete. The `.backup` command above is safe, because it uses the backup interface of SQLite.

The Terraform configurations take one disk snapshot each day at 03:00 UTC and keep 28 of them.
