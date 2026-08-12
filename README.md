# Blogchat

One Go process and one SQLite file. Text only. Invite only. No passwords, email auth only.

Two areas share the same backend:

- **Posts.** A blog thread with a subject and a body. The list sorts on the creation time. All replies stay.
- **Chat.** A channel with a name and an optional topic. The list sorts on the time of the newest message. Each channel keeps the newest messages only and removes the rest.

A channel is a post row with the `is_chat` column set to 1, and a chat message is a reply to that row. There is no separate table.

### Posts

![Posts](docs/posts.png = 250x)

![Chat](docs/chat.png = 250x)

## AI Usage Disclosure

This README was written by Opus 5.0, and all of the code was as well.  And the Terraform configs.  Hand editing will take place as usage testing finds issues, so it remains stable after the initial features are stable.

## Build

```
go mod tidy
go build -o blog .
```

The binary is static. The SQLite driver is pure Go, so the build needs no C toolchain.

## First run

Copy `config.json` and set `site_url` and `mail_from`. The database file does not need to exist; the program creates the file and the schema.

The platform has no registration page, so the first member cannot invite himself. Give the address and the handle of the first member one time:

```
./blog -config config.json -seed-email you@example.com -seed-handle root
```

The program prints a sign-in link to standard output. The link is valid for 24 hours. Later runs need no seed flags.

## Operation

| Signal | Result |
|---|---|
| SIGHUP | Reload the configuration file and the country table |
| SIGTERM, SIGINT | Stop, checkpoint the write-ahead log, close the database |

The listen address and the database path apply at startup only. A change of those two values needs a restart.

To disable a member, set the `enabled` column to 0:

```
sqlite3 blog.db "UPDATE users SET enabled = 0 WHERE handle = 'name'"
```

The member loses access on the next request, because the session query reads the column.

## Chat

Any member makes a channel from the `/chat` page. The channel needs a name.  The topic line is optional and appears under the name on the channel page.  The member that makes the channel owns it and can delete it. A delete of a channel removes every message in it.

Each channel keeps the newest `chat_keep` messages. When a new message arrives, the program adds the message, moves the channel to the top of the list, and removes the messages above the limit. The three operations are in one transaction. **Removed messages are gone and no backup of them exists inside the program.**

The channel page shows the newest `chat_per_page` messages, oldest first, so that the page reads from top to bottom. There are no older pages, because the keep limit already removes the old messages.

A member can delete his own message. The owner of the channel can delete any message in that channel.

Three properties of the limit need attention:

1. The limit applies to each channel and not to the site. A member with 10 channels holds 10 times `chat_keep` messages.
2. A reduction of `chat_keep` does not shorten a quiet channel, because the trim runs only when a new message arrives.
3. A `/p/{id}` address for a channel redirects to `/c/{id}`, and a `/c/{id}` address for a blog post redirects to `/p/{id}`. A wrong link corrects itself.

## Geoblocking

The block list is in `blocked_countries`. The default list is `GB` and `AU`.  Use ISO 3166-1 alpha-2 codes. The code for the United Kingdom is `GB`. The code `UK` is not assigned and blocks nothing.

There are two sources for the country of a request:

1. The `CF-IPCountry` header. The program reads the header only when the peer address is inside a prefix in `trusted_proxies`.
2. A local table. Set `geo_v4_file` to a CSV file with three columns: the first address, the last address, and the two-letter code. The address columns accept the dotted form and the decimal form.

With no proxy and no table, no request is blocked and the program writes a warning at startup.

**This filter is best effort.** VPN services and old allocation data make the country wrong for some clients. The filter removes unwanted traffic. It is not a security control and it is not a legal control.

## Limits

| Item | Value |
|---|---|
| Subject | 200 characters |
| Post body | 16 KB |
| Reply body | 4 KB |
| Request body | 32 KB |
| Sign-in link | 15 minutes, one use |
| Invitation link | 7 days, one use |
| Session | 30 days |
| Sign-in mails | 5 in 15 minutes for each address and each client address |
| Open invitations | 5 for each member |

## Backup

Stop the process and copy `blog.db`, or use the online command:

```
sqlite3 blog.db ".backup backup.db"
```

