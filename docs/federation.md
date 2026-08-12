# Blogchat Federation

A specification for the transfer of published content between Blogchat servers.

## 1. Model

A server holds local content. A member marks a post or a channel for publication. A published item is available to peer servers.

Two servers become peers when the administrator of each one approves the link. There is no directory, no registry, and no third party. A pair is independent of every other pair.

A target server pulls from an origin server. The target stores what it receives as remote content. The target may publish that remote content again. A third server that pulls from the target receives the item as ordinary published content of the target.

The result is that content moves any distance through a chain of pairs. Each hop is a normal publication. No path record travels with the item.

There is no global name space, no shared membership, and no network-wide state. Every decision is local to one server or to one pair.

### 1.1 Properties

| Property | Value |
|---|---|
| Direction of transfer | Pull, target from origin |
| Approval | Both administrators, per pair |
| Provenance in the record | None |
| Reply path to the origin | None |
| Withdrawal after transfer | Not possible |
| Effect of one pair on another pair | None |

A receiver learns the handle, the topic, the subject, the body, and the creation time. A receiver learns that an item is remote to the sending server. A receiver does not learn the origin server, the number of hops, or any member identity.

## 2. Data model

### 2.1 New columns on `posts`

| Column | Type | Default | Function |
|---|---|---|---|
| `topic` | TEXT | NULL | The dotted topic string, section 3 |
| `published` | INTEGER | 0 | 1 when the item is available to peers |
| `pub_seq` | INTEGER | NULL | The publication sequence number, section 5 |
| `is_remote` | INTEGER | 0 | 1 when the item arrived from a peer |
| `peer_id` | INTEGER | NULL | The pair that delivered the item |
| `origin_time` | INTEGER | NULL | The creation time at the first server |
| `content_hash` | BLOB | NULL | The duplicate key, section 6 |

`peer_id` is a local value. It never leaves the server.

`origin_time` holds a Unix time in seconds. The value travels without change through every hop.

### 2.2 New table `peers`

| Column | Type | Function |
|---|---|---|
| `id` | INTEGER PRIMARY KEY | The local identifier of the pair |
| `label` | TEXT | The name the administrator gives the pair |
| `endpoint` | TEXT | The address of the other server |
| `direction` | TEXT | `pull`, `push`, or `both` |
| `our_key` | BLOB | The Ed25519 private key of this side |
| `their_key` | BLOB | The Ed25519 public key of the other side |
| `topic_filter` | TEXT | A glob, NULL means everything |
| `topic_prefix` | TEXT | A prefix added on receipt, NULL means none |
| `pull_cursor` | INTEGER | The highest `pub_seq` received |
| `push_cursor` | INTEGER | The highest `pub_seq` sent |
| `interval_sec` | INTEGER | The time between transfers |
| `enabled` | INTEGER | 0 stops all transfer |
| `last_ok` | INTEGER | The time of the last good transfer |
| `last_error` | TEXT | The text of the last failure |

The key pair is unique to the pair. A server that holds ten pairs holds ten key pairs. No key identifies the server across two pairs.

### 2.3 New table `pub_counter`

One row, one column, `next INTEGER`. The source of `pub_seq` values. Section 5 describes the use.

### 2.4 Indexes

```sql
CREATE INDEX idx_posts_pubseq ON posts(pub_seq) WHERE published = 1;
CREATE INDEX idx_posts_topic ON posts(topic);
CREATE UNIQUE INDEX idx_posts_hash ON posts(content_hash) WHERE content_hash IS NOT NULL;
```

The partial index on `pub_seq` holds only the published rows, so a transfer query reads the smallest possible set.

## 3. Topics

A topic is a lower case dotted string. The rules are:

1. Two segments at least.
2. A dot separates two segments.
3. A segment starts with a letter, `a` to `z`.
4. A segment continues with a letter or a digit, `a` to `z` and `0` to `9`.
5. No other character is valid.
6. No empty segment, no first dot, no last dot.
7. The length is 128 characters at most.

Valid: `games.all`, `games.babylon5`, `alt.rock.and.roll`, `a.b`

Not valid: `games`, `games.5`, `.games.all`, `games.all.`, `games..all`, `Games.All`, `games.all-x`

### 3.1 The validator

```go
// ValidTopic reports whether s is a well formed topic.
func ValidTopic(s string) bool {
	const maxLen = 128

	if len(s) == 0 || len(s) > maxLen {
		return false
	}

	dots := 0
	segStart := true

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.':
			if segStart {
				return false
			}
			dots++
			segStart = true
		case c >= 'a' && c <= 'z':
			segStart = false
		case c >= '0' && c <= '9':
			if segStart {
				return false
			}
		default:
			return false
		}
	}

	if segStart {
		return false
	}
	return dots >= 1
}
```

The caller converts the input to lower case before the call. The validator runs on the input of a member and on every item that arrives from a peer.

### 3.2 Search

The character set of a topic holds no glob character, so a search pattern needs no escape. The SQLite `GLOB` operator gives the match:

```sql
SELECT * FROM posts WHERE topic GLOB ? ORDER BY created DESC;
```

A pattern with a fixed first part, such as `games.*`, uses the index. A pattern that starts with a wildcard reads the table.

## 4. Publication

A post or a channel is not published. This is the state at creation and the state after every edit of the flag.

The owner of a post publishes the post. The owner of a channel publishes the channel. A published channel sends its messages.

A member may set the flag off. The item stops going to peers. Every copy that is already at a peer stays there. **Publication is not reversible.**

The user interface states this at the moment of publication.

## 5. The publication sequence

`pub_seq` is the cursor of every transfer. A row without a value is not transferred.

The rules are:

1. A row takes a `pub_seq` value when `published` changes from 0 to 1.
2. The value comes from `pub_counter.next`. The counter increases by one.
3. The assignment and the increase are in one transaction with the change of the flag.
4. A change from 1 to 0 leaves the value in place.
5. A change from 0 to 1 a second time takes a **new** value.

Rule 5 is the reason the counter is separate from the row identifier. A member writes post 100, leaves it unpublished, and publishes it a day later. The row identifier is below the cursor of every pair, so a transfer on the row identifier never sends it. The publication counter is above the cursor, so the transfer sends it.

```sql
BEGIN IMMEDIATE;
UPDATE pub_counter SET next = next + 1;
UPDATE posts
   SET published = 1,
       pub_seq = (SELECT next FROM pub_counter)
 WHERE id = ?;
COMMIT;
```

`BEGIN IMMEDIATE` takes the write lock at the start, so two publications at the same time cannot take the same number.

## 6. The duplicate key

A chain of pairs can form a ring. A ring returns an item to the server that wrote it. A ring also delivers one item twice through two paths.

`content_hash` stops both. The value is:

    SHA-256( handle || 0x00 || topic || 0x00 || subject || 0x00 || body || 0x00 || decimal(origin_time) )

The fields are UTF-8 bytes. The separator is one zero byte. The time is the decimal form without a sign.

A server computes the hash when it writes a local item and when it receives a remote item. The unique index rejects a second row with the same value. A receiver that finds the value discards the item and moves the cursor forward.

The hash is local. It never travels.

## 7. The transfer

### 7.1 The path

One HTTP endpoint on each server:

    POST /fed/pull

### 7.2 The request

```json
{
  "cursor": 4192,
  "limit": 200
}
```

`cursor` is the highest `pub_seq` the target holds from this pair. The first request sends 0.

`limit` is the count of items the target accepts. The origin may send fewer.

### 7.3 The response

```json
{
  "items": [
    {
      "seq": 4193,
      "kind": "post",
      "handle": "root",
      "topic": "games.babylon5",
      "subject": "The shadow war",
      "body": "Text of the post.",
      "origin_time": 1754870400,
      "remote": false
    }
  ],
  "high": 4193,
  "more": false
}
```

| Field | Function |
|---|---|
| `seq` | The `pub_seq` at the sending server. The target stores this as the cursor. |
| `kind` | `post`, `reply`, `channel`, or `message` |
| `handle` | The handle at the server that wrote the item |
| `topic` | The topic, or null for a reply and a message |
| `subject` | The subject or the channel name, or null for a reply and a message |
| `body` | The text |
| `origin_time` | The creation time at the first server |
| `remote` | True when the item is remote to the sending server |
| `parent_seq` | The `seq` of the parent, for a reply and a message only |

`more` true means the origin holds items above `high`. The target requests again at once.

**`seq` is the number at the sending server and at no other server.** A target that publishes the item again gives it a new number from the local counter.

### 7.4 The order

The origin sends in `pub_seq` order, lowest first. A parent always holds a lower number than a child, because the parent is published first. The target therefore always holds the parent before the child arrives.

### 7.5 Replies and messages

A reply and a message carry `parent_seq`. The target holds a map from the `seq` of the pair to the local row:

**Table `peer_seq_map`**

| Column | Function |
|---|---|
| `peer_id` | The pair |
| `remote_seq` | The number at the other server |
| `local_id` | The row on this server |

The target reads the map to attach the child to the right parent. An item with a `parent_seq` that is not in the map is discarded.

The map is per pair, so the same number from two pairs points to two different rows.

## 8. Authentication

### 8.1 The key pair

Each side makes an Ed25519 key pair when the administrator makes the pair record. The two administrators exchange the public keys by any means outside the program.

The keys belong to the pair. A server with ten pairs holds ten private keys and ten public keys. **No key is common to two pairs**, so two administrators that compare their records cannot tell that they hold a link to the same server.

### 8.2 The signature

The target signs the request. The header is:

    X-Fed-Time: 1754870400
    X-Fed-Sig: base64(Ed25519 signature)

The signed bytes are:

    "POST" || 0x00 || "/fed/pull" || 0x00 || decimal(time) || 0x00 || SHA-256(body)

The origin verifies with the public key of the pair. A time more than 300 seconds from the clock of the origin fails. A failure returns 401 and writes nothing.

The signature covers the transport. An item carries no signature, because an item passes through a chain of servers and a per item signature would identify the first server.

### 8.3 The transport

TLS on the outside. The signature and TLS are separate; TLS gives privacy, the signature gives identity of the pair.

## 9. Receipt

The target performs these steps for each item:

1. **Filter.** Test `topic` against `topic_filter`. A failure discards the item and moves the cursor.
2. **Validate.** Test the topic with `ValidTopic`. Test the lengths against section 12. A failure discards the item and moves the cursor.
3. **Prefix.** Add `topic_prefix` to the front of the topic when the column holds a value.
4. **Hash.** Compute `content_hash` from the fields, before step 3, so that the value is the same at every server.
5. **Store.** Insert with `is_remote` 1, `peer_id` set, `published` 0, `pub_seq` null. A conflict on the hash index discards the item.
6. **Map.** Write the row in `peer_seq_map`.
7. **Cursor.** Set `pull_cursor` to `seq`.

Steps 5, 6, and 7 are in one transaction. A failure of the process leaves the cursor at the last good item, and the next transfer starts there.

A received item is **not published**. The administrator of the target publishes it, and the item then goes to the pairs of the target with a new number.

## 10. Relay

A target that publishes a received item sends it on. The record that leaves holds `remote` true.

The next server knows the item is not of the sending server. The next server does not know the first server, the count of the hops, or the path.

The set of possible first servers is every server that the sending server can reach. A receiver cannot list that set.

**A published item cannot be recalled.** The first server holds no address of any copy. A delete is local to one server.

## 11. Control

### 11.1 The origin

A member publishes or does not publish. Not published is the state at creation.

### 11.2 The pair

Each pair has a topic filter and an interval. The two administrators choose. A pair sends what the pair agrees to send.

### 11.3 The target

The administrator of the target may:

- Delete any received item.
- Set `enabled` to 0 on a pair, which stops the transfer and holds the items already received.
- Delete a pair, which removes every item that arrived through it.
- Set a substring filter that discards an item at receipt.

The substring filter is a convenience. A member that knows the filter avoids it. **The control that works is the removal of the pair.**

### 11.4 The limit of the control

A handle is not unique across servers. Two servers hold a member with the handle `root`. A block on a handle blocks every member with that handle.

Provenance does not travel, so the target cannot separate the items of one first server from the items of another first server inside one pair.

The control available is therefore the pair, and the pair only. This follows from the anonymity of the first server. The two cannot both hold.

## 12. Limits

| Item | Value |
|---|---|
| Topic | 128 characters |
| Subject, channel name | 200 characters |
| Post body | 16 KB |
| Reply body | 4 KB |
| Chat message | 2 KB |
| Handle | 24 characters |
| Items in one response | 200 |
| Response body | 4 MB |
| Signature age | 300 seconds |
| Transfer interval | 60 seconds at least |
| Pairs on one server | 50 |

A server rejects a response above the limit and writes an error. A server that receives an item above a field limit discards the item.

## 13. Operation

### 13.1 To make a pair

1. The administrator adds a row in `peers` with a label and an endpoint.
2. The program makes the key pair and prints the public key.
3. The two administrators exchange the public keys outside the program.
4. Each one writes the key of the other into `their_key`.
5. The administrator sets `enabled` to 1.

### 13.2 The transfer loop

One goroutine for each enabled pair. The loop waits `interval_sec`, then sends one request. A failure writes `last_error` and waits twice the interval, to 1 hour at most. A success writes `last_ok` and resets the wait.

A pair with `direction` `push` runs no loop; the other side pulls.

### 13.3 Signals

SIGHUP reloads the pair records. A change of an interval, a filter, or the enabled flag applies at the next cycle. A new pair starts a loop. A removed pair stops one.

### 13.4 The size of the store

Received content grows with the count of the pairs and the activity of the peers, and the server does not control either one. Two controls exist:

1. A topic filter on the pair.
2. A keep limit for each pair, which removes the oldest received items above a count. The trim runs after a transfer, in the same manner as the chat trim.

## 14. Properties to know

1. **Publication is final.** A copy at a peer stays there. The first server has no address of the copy.
2. **A reply does not return.** A member of the target that replies to a received post writes a local reply. The first server never sees it.
3. **A correction does not follow the error.** A correction is a new item and travels a different path.
4. **A handle is not unique.** Two members with the same handle on two servers are two members.
5. **The peer is the unit of control.** A drop of a pair removes good content with bad content.
6. **The topic string is the only shared name.** Two servers agree on a topic by choosing the same string. Nothing enforces the agreement, and a prefix on the pair corrects a difference at one server.
7. **A ring is safe.** The hash index stops the loop. The cost is one lookup for each item.

## 15. Order of work

| Stage | Content |
|---|---|
| 1 | The topic column, the validator, the search |
| 2 | The publication flag, the counter, the sequence |
| 3 | The peer table, the keys, the administration pages |
| 4 | The pull endpoint, the signature, the receipt |
| 5 | The transfer loop, the retry, the signal handling |
| 6 | The relay flag, the hash index |
| 7 | The filter, the prefix, the keep limit |

Stages 1 and 2 are useful without any pair. A server runs them and gains topics and a publication flag. Stage 3 and above add the transfer.

---

# Blogchat Federation — Appendices

Supporting material for the specification. Nothing here repeats section 1 to 15. Each appendix states which section it supports.

---

## Appendix A — State transitions of an item

Supports section 4 and section 5.

The specification states the rules of the flag and the counter. This table gives every transition and the effect on the transfer.

| From | To | `pub_seq` | Effect on the pairs |
|---|---|---|---|
| Created | Not published | NULL | No transfer. The item is invisible outside the server. |
| Not published | Published | New value from the counter | The item goes to every pair on the next cycle. |
| Published | Not published | Held, unchanged | The item stops going to a pair with a cursor below the value. A pair with a cursor above the value already sent it. |
| Not published, held value | Published | **New** value, higher than the first | Every pair sends the item again. A pair that sent the first time sends a second time. |
| Published | Deleted | Row gone | No transfer. Every copy at a peer stays. |
| Received from a peer | Stored | NULL | No transfer. The administrator decides. |
| Received | Published | New value from the local counter | The item relays with `remote` true. |

The fourth row is the reason for a separate counter. A member that publishes, withdraws, and publishes again sends the item twice to a peer that was slow. The peer holds one copy, because the hash index rejects the second. The cost is one wasted transfer.

---

## Appendix B — Field travel

Supports section 7.3 and section 10.

Which values leave the server, which values stay, and which values change at a hop.

| Field | Leaves | Changes at a hop | Note |
|---|---|---|---|
| `handle` | Yes | No | The handle of the first server. Not unique. |
| `topic` | Yes | Only by `topic_prefix` | Section 9 step 3. |
| `subject` | Yes | No | |
| `body` | Yes | No | |
| `origin_time` | Yes | No | Set at the first server, held to the last. |
| `remote` | Yes | Yes | False at the first server, true at every hop after. |
| `seq` | Yes | Yes | The number of the sending server only. |
| `parent_seq` | Yes | Yes | Resolved through `peer_seq_map`. |
| `content_hash` | No | — | Computed at each server from the fields. Identical everywhere. |
| `peer_id` | No | — | Local label of the pair. |
| `pub_seq` | Local only | — | The `seq` field carries the value outward. |
| Member email | No | — | No page shows it, and no transfer carries it. |
| Invite lineage | No | — | Local to the server. |
| Server identity | No | — | Nothing carries it. |
| Public key | No | — | Used in the transport, not in the record. |
| Hop count | No | — | Not held and not derivable. |

The last four rows are the anonymity of the first server. Every one of them must stay false for the property in section 10 to hold. A change that adds any of them to the record removes the property.

---

## Appendix C — What an observer learns

Supports section 8.1 and section 10.

Five positions, and the information at each one.

| Position | Learns | Does not learn |
|---|---|---|
| A reader on the target | Handle, topic, subject, body, origin time, that the item is remote to this server | The first server, the path, the hop count, the member |
| A peer administrator | Everything a reader learns, plus the endpoint of the pair, the public key of the pair, the transfer times | The other pairs of that server, the first server of a relayed item |
| Two peer administrators that compare records | That the two endpoints differ or match | That the two pairs reach the same server, when the endpoints differ. The keys are per pair and do not match. |
| A network observer between the pair | The two addresses, the times, the sizes | The content, under TLS |
| A person that holds the mailbox of a member | Full access to that account | Nothing about the federation, unless that member is the administrator |

The third row states the limit of the pairwise key. The keys give no correlation. **The endpoint does.** Two administrators that both connect to the same host name know they hold a link to the same server. Separate host names for separate pairs close this. The specification does not require it, because the pair is a chosen relationship and the two sides usually know each other.

---

## Appendix D — Failure modes

Supports section 13.2 and section 14.

| Event | Detection | Effect | Action |
|---|---|---|---|
| The origin is offline | Connection failure | The cursor holds. Nothing is lost. | The loop retries with a growing wait. |
| The origin changes its address | Connection failure | Transfer stops | The administrator edits `endpoint`. |
| The signature fails | 401 at the origin | No transfer, no write | Check the clock. Check the keys. |
| The clock is more than 300 seconds out | 401 at the origin | All transfer stops | Run NTP. This is the most common cause of a total failure. |
| The database of the origin is restored from a backup | The cursor of the target is above the counter of the origin | The target receives nothing further | Section E. |
| A peer sends an item above a field limit | Length test at step 2 | The item is discarded, the cursor moves | None. The loss is one item. |
| A peer sends an invalid topic | `ValidTopic` at step 2 | The item is discarded, the cursor moves | None. |
| A `parent_seq` is not in the map | Map lookup at step 5 | The child is discarded | The parent was filtered or discarded. The loss is intended. |
| The disk is full | Insert failure | The transaction rolls back, the cursor holds | Free space, or set a keep limit for the pair. |
| A ring returns an item | Conflict on the hash index | The item is discarded, the cursor moves | None. Working as intended. |
| Two servers use the same handle | Not detected | Two authors show one name | Nothing at the protocol. A local rename of the received item is possible. |
| A peer relays unwanted content | A reader reports it | Present on the server | Delete the item, or drop the pair. |

---

## Appendix E — Recovery of a cursor

Supports section 7.2 and appendix D row 5.

The cursor is the only shared state of a pair, and it is held twice, once at each side. A restore from a backup breaks the agreement.

| Case | Symptom | Repair |
|---|---|---|
| The target loses its cursor, value too low | Items arrive a second time | None needed. The hash index discards them. The cost is bandwidth. |
| The target loses its cursor, value too high | Items are skipped | Set `pull_cursor` to 0. Every item arrives again and the hash index keeps one copy of each. |
| The origin restores an old database | The counter is below the cursor of the target. The target receives nothing. | The origin raises `pub_counter.next` above the highest value it ever issued. A safe value is the old value plus a large margin. |
| The origin reuses a counter value | Two different items hold one number at the origin. The target maps one and discards the other. | Prevented by the repair above. The counter must never go backward. |

**The rule: the publication counter never decreases.** A backup restore is the only event that can break this, and the administrator raises the counter by hand after such a restore.

Setting `pull_cursor` to 0 is always safe and always correct. The cost is a full transfer. This is the general repair for any doubt about a pair.

---

## Appendix F — Size and rate

Supports section 13.4.

One item costs, in the store: the body, plus the subject, plus 32 bytes of hash, plus about 100 bytes of row and index. A post of 2 KB costs about 2.2 KB.

| Peers | Members on each peer | Posts each day for each member | Items each day | Store each year |
|---|---|---|---|---|
| 5 | 20 | 1 | 100 | 80 MB |
| 10 | 20 | 1 | 200 | 160 MB |
| 10 | 20 | 5 | 1,000 | 800 MB |
| 20 | 50 | 5 | 5,000 | 4 GB |

A relay chain raises the count without raising the peer count, because a peer sends what it received from its own peers. The table above holds only when no peer relays. With relay, the arriving volume is the volume of the whole reachable set, and the server does not control it.

Two controls, from section 11.2 and 13.4:

| Control | Effect | Cost |
|---|---|---|
| `topic_filter` on the pair | Reduces at receipt, before the store | The filter is a glob, so it selects a subtree only |
| Keep limit for each pair | Bounds the store | Old received items are removed. They cannot be recovered without a full transfer. |

A full transfer after a keep limit refills the store to the limit. The two work against each other, so a pair with a keep limit should not be reset to cursor 0 without cause.

---

## Appendix G — Reading load

Supports section 14 and the reason for section 3.

The technical limits in appendix F are comfortable. The limit that binds first is the reader.

| Items arriving each day | Effect on a reader |
|---|---|
| 50 | A reader reads everything. |
| 500 | A reader skims the list. |
| 2,000 | A reader reads one topic subtree only. |
| 20,000 | A list in time order is not usable. Topic selection is necessary. |

This is the reason the topic column exists. A glob over a subtree turns one large list into several small ones, and time order inside a subtree stays usable at any volume the store can hold.

The order for a list page is:

| Mode | Order | Signal |
|---|---|---|
| Newest | `created DESC` | The time of the item on this server |
| Recently active | The newest reply time, descending | Local replies only |

The second mode ranks on the interest of the members of this server, because a reply does not return to the first server. This is correct for a local reader and carries no meaning for any other server.

---

## Appendix H — Comparison with the prior systems

Supports the design decisions in section 6, 7, and 10.

| Property | Usenet | Fidonet echomail | Blogchat federation |
|---|---|---|---|
| Injection cost | Any feed, no relationship | Admission by a coordinator | One administrator approves one pair |
| Global directory | The hierarchy and the newgroup process | The nodelist and the coordinators | None |
| Message identity | Message-ID, global | MSGID, global | None. A local hash only. |
| Loop control | The Path header | SEEN-BY and PATH lines | Re-origination and the hash index |
| Path visible to a reader | Yes | Yes | No |
| Forwarding | Flood fill, all peers | Flood fill inside an echo | Pull, and republication by choice |
| Cursor | None, push of new articles | None, packet based | One integer for each pair |
| Retraction | Cancel messages, forgeable | Local only | Local only |
| Cause of decline | Open injection, then binaries | Loss of purpose, not abuse | — |

Two design points follow.

**Re-origination replaces path tracking.** Every hop is an ordinary local publication, so one integer for each pair replaces the path list of both prior systems. This is the reason no global identity is needed, and it is the reason the path cannot be read.

**A relationship gates injection.** This is the difference from Usenet that matters. The failure that ended the Usenet text hierarchy needed injection at no cost and with no relationship. Neither holds here.

---

## Appendix I — Threat table

Supports section 11.4.

| Threat | Available control | Works |
|---|---|---|
| A member of this server writes spam | Set `enabled` to 0 on the member. Remove the member that invited him. | Yes |
| A peer sends spam | Drop the pair | Yes, and it removes good content with the bad |
| A peer sends one bad author among good ones | Substring filter, or drop the pair | Partly. The substring filter is avoidable. The pair drop is heavy. |
| A first server sends bad content through a good peer | Drop the pair | Yes at this server, and the peer must act for itself |
| An attacker forges a record | The pair signature | Yes at the pair. A dishonest peer can still send anything. |
| An attacker replays a request | The 300 second window | Yes |
| An attacker joins to map the network | Nothing carries topology | The attacker learns the peers he holds and no others |
| An attacker publishes to force a store to fill | The keep limit and the topic filter | Yes, with a bound on the damage |
| An attacker seeks the first server of an item | Nothing carries it | Yes. The set of candidates is the reachable set. |
| An attacker seeks the identity of a member | Only the handle travels | Yes at the protocol. The body may reveal it. |

The last row is the residual risk and it is not technical. A body written by a person identifies that person by style, by subject, and by reference to local events. The protocol protects the server. It does not protect a writer against analysis of what the writer wrote.

---

## Appendix J — Legal position

Supports section 11 and the choice of the receipt model.

| Role | Definition | This design |
|---|---|---|
| Publisher | Selects and issues content, liable for all of it | The local posts of the members |
| Distributor | Carries the content of others, liable on notice | The received content of the peers |
| Common carrier | Carries all traffic, no selection | Not this. The administrator selects the pairs and may delete any item. |

A received item is stored unpublished and an administrator acts to publish it. That act is a selection, so a server is a distributor of the content of its peers and a publisher of the content of its members.

The distributor position rests on *Smith v. California* (1959) and is the position of a bookstore. It carries a duty to act on notice and no duty to inspect in advance. It needs no change of statute, no common carrier theory, and no court to revisit the holding in *Moody v. NetChoice* (2024) that curation is protected expression.

Two operational consequences:

1. An administrator that receives a notice about a received item must be able to find and delete it. The `peer_id` column and the topic index make this a single query.
2. The unpublished default at receipt is what places the administrator in the distributor role rather than the carrier role. An automatic publication of received content weakens the position and should not be added.

This is general information about the doctrine, not legal advice, and the position of a server depends on its jurisdiction.

---

## Appendix K — Test cases

Supports section 3.1, 5, 6, and 9.

**Topic validator**

| Input | Result | Rule |
|---|---|---|
| `games.all` | Valid | |
| `games.babylon5` | Valid | A digit after a letter |
| `a.b` | Valid | The shortest valid form |
| `alt.rock.and.roll` | Valid | |
| `games` | Invalid | One segment |
| `games.5` | Invalid | A segment starts with a digit |
| `.games.all` | Invalid | A first dot |
| `games.all.` | Invalid | A last dot |
| `games..all` | Invalid | An empty segment |
| `Games.All` | Invalid | Upper case |
| `games.all-x` | Invalid | A character outside the set |
| `games all` | Invalid | A space |
| `` (empty) | Invalid | |
| `.` | Invalid | |
| 129 characters | Invalid | Above the length |

**Sequence**

| Case | Expected |
|---|---|
| Publish post A, then post B | The value of B is above the value of A |
| Create A, create B, publish B, publish A | The value of A is above the value of B |
| Publish A, withdraw A, publish A | The third state holds a new and higher value |
| Two publications at the same moment | Two different values, from `BEGIN IMMEDIATE` |

**Hash and rings**

| Case | Expected |
|---|---|
| The same item arrives from two pairs | One row, the second insert is rejected |
| An item returns through a ring to its first server | The insert is rejected |
| Two items with the same body and a different `origin_time` | Two rows |
| Two members with the same handle on two servers, the same body | One row is rejected. **This is a false match.** |

The last row is a known and accepted collision. Two different people with the same handle that write identical text at the same second lose one copy. The probability is negligible and the alternative is a global identity, which removes the anonymity of the first server.

**Receipt**

| Case | Expected |
|---|---|
| An item fails `topic_filter` | Discarded, the cursor moves |
| A child arrives after the parent was filtered | The child is discarded |
| The transfer stops in the middle of a batch | The cursor holds at the last committed item |
| `topic_prefix` is set | The stored topic holds the prefix, and the hash uses the topic without it |

The last row matters. The hash is computed before the prefix, so the same item at two servers with different prefixes gives the same hash. Without this, a ring through two servers with different prefixes would not be detected.
