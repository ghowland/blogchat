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

---

# Addendum 1 — Relay is automatic

Corrects section 9 step 5, section 10, section 11.3, and appendix J. Those sections state that an administrator publishes each received item by hand. That is wrong. Nothing in the design requires it, and a per-item human step does not scale past a few peers.

**Relay is a property of the pair, set once when the link is made. It applies to every item that arrives through that pair, with no further action.**

## 1. Replaces section 9 step 5

A received item is stored with `is_remote` 1 and `peer_id` set. The publication state comes from the pair:

| `peers.relay` | Effect at receipt |
|---|---|
| 0 | Stored with `published` 0 and `pub_seq` NULL. The item is readable on this server and goes no further. |
| 1 | Stored with `published` 1 and a new `pub_seq` from the local counter, in the same transaction. The item goes to every other pair on the next cycle. |

The default for a new pair is 0. The administrator sets it to 1 to make the pair a relay. This is one decision at the time of the link, not a decision for each item.

## 2. New column on `peers`

| Column | Type | Default | Function |
|---|---|---|---|
| `relay` | INTEGER | 0 | 1 makes every item from this pair relay onward |

## 3. Replaces section 9, the transaction

Steps 5, 6, and 7 become one transaction that also assigns the sequence when the pair relays:

```sql
BEGIN IMMEDIATE;
-- when peers.relay = 1
UPDATE pub_counter SET next = next + 1;
INSERT INTO posts (handle, topic, subject, body, origin_time,
                   is_remote, peer_id, content_hash,
                   published, pub_seq)
VALUES (?, ?, ?, ?, ?, 1, ?, ?,
        1, (SELECT next FROM pub_counter));
INSERT INTO peer_seq_map (peer_id, remote_seq, local_id) VALUES (?, ?, last_insert_rowid());
UPDATE peers SET pull_cursor = ? WHERE id = ?;
COMMIT;
```

When `relay` is 0, the two counter statements are omitted and the insert writes `published` 0 and `pub_seq` NULL.

A conflict on the hash index rolls the whole transaction back except the cursor update. The counter value is consumed and skipped. Gaps in the sequence are harmless, because the cursor is a high water mark and not a count.

## 4. Replaces section 11.3

The administrator does not act on each item. The administrator acts on the pair and on exceptions:

| Control | Scope | When |
|---|---|---|
| `relay` 0 or 1 | Every item of the pair | Set at the time of the link |
| `topic_filter` | Every item of the pair | Set at the time of the link |
| `enabled` 0 | The pair | On a problem |
| Delete a pair | Every item received through it | On a problem |
| Delete an item | One item | On a report |

Deletion of an already relayed item is local, as section 14 states. The onward copies stay.

## 5. Replaces the relay paragraph in section 10

An item propagates without any human in the path. A chain of pairs with `relay` 1 carries an item the full length of the chain at the speed of the sync intervals. The onward record holds `remote` true and a new `seq` from each server in turn.

This is the property that makes the design a federation rather than a set of manually mirrored servers.

## 6. Corrects appendix J

The statement that the administrator performs a selection on each item is wrong, and the legal reasoning built on it was wrong with it.

The correct statement: the administrator selects the **peers** and the **topic filter**, and content passes automatically inside those choices. This is the same posture as an internet service provider, a mailing list relay, and a Usenet server, all of which have been treated as distributors rather than publishers of the traffic they carry. The distributor position under *Smith v. California* (1959) rests on liability arising at notice, not on inspection in advance — a bookstore is a distributor precisely because it does **not** read every book.

Automatic relay therefore strengthens the distributor position rather than weakening it. The earlier appendix had it backwards. The operational requirement stays the same: on notice, the administrator must be able to find and delete an item, which the `peer_id` column and the topic index support.

This is general information about the doctrine and not legal advice.

## 7. Corrects appendix D

Add:

| Event | Detection | Effect | Action |
|---|---|---|---|
| A relay pair carries unwanted content onward before anyone reads it | A report from a downstream server | Copies exist beyond reach | Set `relay` 0 on the pair, or drop the pair. Past copies stay. |

This is the cost of automatic relay and it is inherent. A server that relays automatically will at some point have relayed something its administrator would not have chosen. The controls are the pair and the filter, applied before the fact, because no control exists after it.

## 8. Corrects appendix F

Automatic relay is the reason the volume table understates the arriving load. With `relay` 1 on both sides of a chain, the volume at any server is the published volume of the whole reachable set, not the volume of its direct peers. The two bounds in appendix F, the topic filter and the per-pair keep limit, are therefore not optional at any significant peer count. They are the only things that bound the store.

---

# Addendum 2 — Topic filter lists, per direction

Corrects section 2.2, section 9 step 1, section 11.2, and appendix F. The specification defines `topic_filter` as one glob on one pair record. That is too coarse. A pair needs a list of globs, and the list is per direction.

## 1. Replaces the `topic_filter` column

`peers.topic_filter` is removed. A new table holds the lists.

**Table `peer_filters`**

| Column | Type | Function |
|---|---|---|
| `peer_id` | INTEGER | The pair |
| `direction` | TEXT | `in` or `out` |
| `pattern` | TEXT | One glob |
| `ord` | INTEGER | The order of evaluation |

A pair holds N rows for `in` and M rows for `out`. Both counts may be zero.

## 2. Semantics

An item matches the list when it matches **any** pattern in the list. The list is a set of alternatives, not a sequence of rules, so the order has no effect on the result. `ord` exists for stable display only.

An empty list means **nothing passes** in that direction. This is the safe default. An administrator that wants everything writes one line:

```
*
```

The evaluation uses the SQLite `GLOB` operator, or an equivalent function in Go. The topic character set holds no glob character, so no escape is needed.

## 3. The two directions

| Direction | Applied by | Applied to | Effect |
|---|---|---|---|
| `out` | The origin, when it builds a response | Items it would send | The origin decides what it offers this pair |
| `in` | The target, at receipt, section 9 step 1 | Items that arrive | The target decides what it keeps |

The two lists are independent and neither side sees the other. An item passes only when it matches both, so the effective set is the intersection. Each side can narrow without asking the other.

The `out` list is the more useful of the two, because it saves bandwidth. An origin that filters on send transfers nothing the target would discard. The `in` list stays necessary as a defence, because a peer may not filter correctly.

## 4. Replaces section 9 step 1

1. **Filter.** Test the topic against the `in` list of the pair. No match discards the item and moves the cursor.

## 5. The origin side, replaces the transfer query

```sql
SELECT id, pub_seq, kind, handle, topic, subject, body, origin_time, is_remote
  FROM posts
 WHERE published = 1
   AND pub_seq > ?
   AND EXISTS (
       SELECT 1 FROM peer_filters
        WHERE peer_id = ? AND direction = 'out'
          AND posts.topic GLOB pattern
   )
 ORDER BY pub_seq ASC
 LIMIT ?;
```

The cursor still moves over the full sequence, not over the filtered set. The response carries `high` as the highest `pub_seq` examined, not the highest sent, so a batch that matches nothing still advances the cursor. Without this a pair with a narrow filter would re-examine the same rows on every cycle.

**This changes the `high` field of section 7.3.** `high` is the highest sequence the origin examined in this batch. `more` is true when rows exist above `high`.

## 6. The pattern of a hub

This is the arrangement the lists are for.

A hub peers widely with `in` set to `*` and `relay` 1. It accumulates everything reachable and offers all of it.

A reader server peers with the hub only, and sets `in` to the subtrees it wants:

```
games.turnbased.*
games.strategy.*
tools.zig.*
```

The hub carries the aggregation cost. The reader server carries only what it reads. Neither one needs any agreement about names beyond the strings themselves.

| Role | `in` list | `out` list | `relay` | Store |
|---|---|---|---|---|
| Hub | `*` | `*` | 1 | Everything reachable |
| Reader server | Narrow list | Local topics only | 0 | Local plus the chosen subtrees |
| Publisher only | Empty | Own topics | 0 | Local only |
| Private pair | Agreed subtree | Agreed subtree | 0 | Local plus that subtree |

A hub is not a privileged node. It holds no authority, approves nothing for anyone, and any server may become one or stop being one. Several hubs may exist with different coverage, and a reader server may peer with more than one. Dropping a hub costs the reader server nothing but the feed.

The `out` list is what lets a server be a hub for some peers and not for others. A hub may offer `*` to a trusted peer and `games.*` to another, from the same store.

## 7. Interaction with `topic_prefix`

Order at receipt: the `in` list is tested against the topic **as it arrived**, then the prefix is added. The filter and the sender therefore agree on what the pattern means.

An administrator that adds a prefix must remember that the local reading globs then need the prefix, while the pair filter does not.

## 8. Corrects appendix F

The topic filter row of the controls table now reads: the `out` list bounds what a pair transfers, and the `in` list bounds what a pair stores. Because the lists are per direction, a server can accept a wide feed from one peer and offer a narrow one to another, so the arriving volume and the offered volume are separately controlled.

An empty `in` list is the default for a new pair, so a newly approved pair transfers nothing until the administrator writes at least one pattern. This is deliberate. A pair that is approved by accident carries nothing.

## 9. Limits

| Item | Value |
|---|---|
| Patterns in one list | 64 |
| Length of a pattern | 128 characters |
| Characters valid in a pattern | The topic set, plus `*`, `?`, `[`, `]`, `-` |

A pattern that holds any other character is rejected when the administrator saves it.

---

# Addendum 3 — Scale

Extends the federation specification. Records the resource behaviour of the design and the properties that change with the count of servers. Written because the resource questions have obvious answers that are repeatedly assumed to be otherwise.

---

## 1. The store is a window, not an archive

Every server holds a fixed count of items and removes the oldest. This is the same mechanism as the chat trim in the base specification, applied to received content.

**There is no growth curve.** The store reaches its steady state at the moment the window fills and stays there. The count does not depend on the count of servers, the count of pairs, the age of the network, or the volume of the reachable set.

The quantity that changes with the volume of the network is not the size of the store. It is the wall clock time the window covers. A busier network gives a shorter history at the same item count.

### 1.1 The arithmetic

| Item | Value |
|---|---|
| Typical post | ~1 KB |
| Chat message | ~150 bytes |
| Field limit, post body | 16 KB |
| 100,000 items at 1 KB | 100 MB |
| 1,000,000 items at 1 KB | 1 GB |

A relay holding 100,000 items holds 100 MB. This fits in the RAM of the smallest instance any provider sells. The SQLite file is durability for a restart. In normal operation the working set is resident and no read reaches the disk.

**Storage is not a constraint of this design at any server count the design reaches.** A specification of the store belongs in the window count, not in a capacity plan.

### 1.2 The window is the tuning control

An administrator sets one number. That number, together with the accepted topics, determines the history depth:

| Accepted topics | Window at 100,000 items |
|---|---|
| `*` | Hours to days |
| `games.*` | Weeks |
| `games.babylon5` | Months to years |

A narrow filter buys history depth. This is the trade an administrator actually makes, and it is a good one: a group with a specific interest wants deep history in its own topic and no history at all in every other topic.

---

## 2. Bandwidth is not a constraint

The content is text. The field limits in section 12 of the federation specification cap a post body at 16 KB and a chat message at 2 KB. A typical item is about 1 KB.

A server sends each item once for each pair. At the cap of 50 pairs, one item costs 50 KB of egress. A server relaying 10,000 items in a day at the pair cap sends 500 MB in that day, which is under 50 kbit/s averaged.

| Case | Egress |
|---|---|
| Median server, 3 pairs, 1,000 items/day | 3 MB/day |
| Relay, 20 pairs, 10,000 items/day | 200 MB/day |
| Relay at the pair cap, 10,000 items/day | 500 MB/day |

These figures are inside the included allowance of every instance in the cost table of the base specification. **Bandwidth does not enter the design of this system.** A future analysis that treats the multiplication by pair count as a limit has mistaken text for media.

---

## 3. The cost of a relay is the pairing, not the hardware

A relay is a server with a broad filter and a high pair count. Sections 1 and 2 give its resource cost: a fixed store of a few hundred megabytes and an egress of a few hundred megabytes a day.

**A relay runs on the cheapest instance available.** There is no hardware reason for relays to be scarce, and no reason to treat relay operation as a burden borne by a few volunteers.

The cost of a relay is one out-of-band key exchange for each pair, performed by a person. That, and the pair cap of 50, are the only limits on relay capacity.

Two consequences:

- Relays are numerous, because they are nearly free to run.
- The loss of any one relay is not an event. Replacement is a new instance and a set of key exchanges.

---

## 4. The topic filter is the routing mechanism

The `topic_filter` glob on each pair is not a control for disk pressure. It is how the network routes.

Each server declares, for each pair, which topics it accepts. The union of those declarations is the topology. There is no single graph:

**There is one graph for each topic, overlaid on the same servers and the same pairs.**

- A server accepting `games.*` on one pair and `music.*` on another belongs to two networks that share its hardware and nothing else.
- An item under `games.babylon5` travels only the subgraph that accepts it. A server outside that subgraph spends nothing on it — no transfer, no store, no window pressure.
- Two servers may hold a pair and still be disconnected for a given topic.

### 4.1 The effect of growth

More servers does not mean a denser network. It means a more selective one. Each new administrator accepts the slice their members read, so the subgraph for any one topic stays proportionate to the interest in that topic rather than to the size of the network.

This is the property that makes the design indifferent to the server count. Growth in servers that do not accept a topic has no effect on any server that does.

---

## 5. Propagation

Propagation time is the sum of the sync intervals along the path, plus up to one interval of phase offset at each hop.

| Servers | Diameter within a topic subgraph | Time at 60 s intervals |
|---|---|---|
| 100 | 3–4 hops | 3–8 minutes |
| 1,000 | 4–5 hops | 4–10 minutes |

Path length grows with the logarithm of the server count. A tenfold growth in the network adds approximately one hop.

The graph is not designed. It grows the way trust grows, because each pair is a relationship between two administrators who already know each other. This produces high clustering and short paths. The routing behaviour is a consequence of that structure and not of any routing decision.

---

## 6. Flooding

A hostile server publishes at volume. The item reaches every server in the subgraph that accepts its topic.

Three properties bound the damage:

**The filter bounds the reach.** A flood under `games.*` does not touch a server that accepts only `music.*`. The flood is confined to the subgraph that asked for the topic.

**The window clears it.** The flood ages out of every store at the same rate as everything else. There is no cleanup operation, no moderation queue, and no administrator action required for the content to leave. **The sliding window makes the network self-healing against volume attacks.**

**The lasting cost is history, not storage.** What the flood destroys is the window depth of the servers it reached, for the period it ran. The store size never changes.

### 6.1 The asymmetry that remains

The cost of the attack is constant. The cost of the defence rises with distance from the source, because provenance does not travel and an administrator can only cut the pair that fed them — which carries legitimate content from a whole region.

The mitigations are the filter and the window above, and the pairing requirement itself. A flooder needs a peer, and a peer is a person who performed a key exchange. The out-of-band exchange is the filter that operates before the fact.

---

## 7. What actually changes with scale

Three things. None is a resource.

**7.1 The topic namespace becomes contested.** The dotted topic string is the only shared name in the system. Nothing enforces agreement on it, and it determines routing. Two groups using different strings for one subject are two networks. Two groups using one string for different subjects collide in every filter that accepts it. At a small server count this is settled by conversation. At a large one, competing strings for one subject are permanent.

`topic_prefix` is the tool that bridges them. One server maps an incoming topic onto another string at receipt. This merges two topic communities without the agreement of either, by the unilateral decision of one administrator.

**7.2 Pairing is the only rate limiter.** Each link costs a human key exchange. The growth rate of the network is bounded by the rate at which trust relationships form and by nothing technical. This is the governor of the design and it is the correct one.

**7.3 Discovery has no mechanism.** There is no directory by construction. At a small server count an administrator knows the people they want to pair with. At a large one they do not. In practice discovery moves into the content: an endpoint and a public key published under an agreed topic, relayed like any other item, read by an administrator who then performs the exchange out of band. Nothing in the program supports this and nothing needs to.

---

## 8. Read at distance, discuss locally

Stated here because it is the reason several of the properties above hold.

The design carries publications. It does not carry replies. A member who reads an item from six hops away discusses it on their own server, and that discussion stays there. The same item relayed to fifty servers produces fifty independent local conversations that never merge.

This removes the entire class of problems that cross-server threading creates: distributed identity, reply attribution, thread state agreement, partial thread views, and per-item signatures.

The absence of the reply path is what permits the absence of provenance, and the absence of provenance is what makes the first server unidentifiable. **These are one decision, not three.** A future change that adds a reply path removes the other two properties with it.

---

## 9. Summary table

| Property | 100 servers | 1,000 servers |
|---|---|---|
| Store per server | Fixed by the window | Identical |
| Bandwidth | Trivial | Trivial |
| Relay hardware cost | Near zero | Near zero |
| Diameter within a topic | 3–4 hops | 4–5 hops |
| Propagation | 3–8 min | 4–10 min |
| Window depth, broad filter | Longer | Shorter |
| Window depth, narrow filter | Unchanged | Unchanged |
| Effective topology | One graph per topic | Many graphs per topic, more selective |
| Flood | Self-clears | Self-clears |
| Growth governor | Human pairing | Human pairing |

**Nothing technical changes between these two columns.** The window depth for broad-filter servers shortens, which moves administrators toward narrower globs, which separates the network into topic subgraphs. That separation is the design working, not the design degrading.

A network of a thousand servers is not one large network. It is several hundred small ones sharing infrastructure, which is the correct shape for a federation of independent groups.

---

# Addendum 4 — Handle block lists, per pair and per direction

Corrects section 9 step 1, section 11.3, section 11.4, and appendix I. The specification states that the pair is the only control available at receipt. That is too narrow. An administrator holds a list of blocked handles on a pair and blocks any number of them without dropping the pair.

## 1. New table `peer_handle_blocks`

| Column | Type | Function |
|---|---|---|
| `peer_id` | INTEGER | The pair |
| `direction` | TEXT | `in` or `out` |
| `handle` | TEXT | One blocked handle, lower case |
| `added` | INTEGER | The time the administrator added the row |
| `note` | TEXT | Free text for the administrator |

The primary key is `(peer_id, direction, handle)`.

A pair holds N rows for `in` and M rows for `out`. Both counts may be zero. The structure matches the glob lists of addendum 2: a list per direction per pair, edited as a set of lines.

## 2. Semantics

An item is discarded when its `handle` matches **any** row in the list for that pair and direction. The match is exact and case-insensitive. There is no glob, because a handle is a name and not a tree.

An empty list blocks nothing. This is the opposite default from the glob lists of addendum 2, and it is correct: a glob list states what a pair carries, and a block list states the exceptions to it.

| Direction | Applied by | Effect |
|---|---|---|
| `in` | The target, at receipt | None of the listed handles enters this server through this pair |
| `out` | The origin, when it builds a response | This pair is offered nothing from the listed handles |

The `out` list blocks local members of the origin server from reaching one specific peer, while those members still reach every other peer. The `in` list blocks incoming names.

## 3. Order of evaluation, replaces section 9 step 1

1. **Block.** Test `handle` against the `in` block list of the pair. A match discards the item and moves the cursor.
2. **Filter.** Test `topic` against the `in` glob list of the pair. No match discards the item and moves the cursor.

The block list runs first. A blocked handle is discarded whatever its topic.

On the origin side the `out` list is added to the transfer query of addendum 2:

```sql
AND posts.handle COLLATE NOCASE NOT IN (
    SELECT handle FROM peer_handle_blocks
     WHERE peer_id = ? AND direction = 'out'
)
```

The cursor still moves over the full sequence. `high` remains the highest sequence examined, so a batch that is entirely blocked still advances the pair.

## 4. Collisions are accepted

A handle is not unique across servers, and provenance does not travel, so a listed handle blocks every member with that name that arrives through that pair, from any server in the reachable set behind it.

This is a known and accepted property. The reasoning:

- The alternative is a globally unique author identity, which would carry the first server and destroy the anonymity in section 10.
- The list is per pair, so the collateral loss is limited to one link and affects no other pair and no other server.
- The administrator removes a line at any time.
- At the scale this design targets, a handle collision inside one feed is uncommon, and the cost when it happens is the loss of one writer's posts on one server.

**Nothing detects a collision and nothing warns of one.** An administrator that blocks a common handle should expect to lose unrelated writers with that name. The `note` column exists so the administrator records why a line is there.

## 5. Effect on existing content

A list applies to items that arrive after the line is added. It does not remove items already stored. An administrator that wants both adds the lines and then deletes the existing rows:

```sql
DELETE FROM posts
 WHERE peer_id = ?
   AND handle COLLATE NOCASE IN (?, ?, ?);
```

Items already relayed onward are beyond reach, as section 14 states.

## 6. Blocks and relay

When `relay` is 1, a blocked handle is discarded at receipt and therefore never takes a `pub_seq` and never goes onward. A block on an inbound pair removes those handles from everything this server relays.

A server that relays and blocks is narrowing the feed for every server behind it. That is a local decision with reach, and it is consistent with the rest of the design: no server can force another to carry anything, and no server can force another to drop anything.

## 7. Corrects section 11.4

The statement that the pair is the only working control is replaced. The controls at receipt, from coarse to fine:

| Control | Scope | Collateral |
|---|---|---|
| Drop the pair | Every item ever received through it | Total for that link |
| `enabled` 0 | Future items of the pair | Holds what is stored |
| Glob list, addendum 2 | Topic subtrees of the pair | Whole subtrees |
| Handle block list | Named handles of the pair | Every writer with those names behind that pair |
| Delete an item | One item | None |

The handle list is the finest control that operates before storage. The substring filter of section 11.3 stays a convenience only, since a writer who knows it avoids it, while a handle block cannot be avoided without changing handle — and a changed handle is a new name that the administrator can add to the list.

## 8. Corrects appendix I

The row "a peer sends one bad author among good ones" is revised. The control is the `in` handle block list, it works, and its cost is the collision property of section 4 rather than the loss of the whole pair.

## 9. Limits

| Item | Value |
|---|---|
| Handles in one list | 1,000 |
| Length of a handle | 24 characters, as section 12 |

The lists are held in memory and reloaded on SIGHUP with the pair records, so a lookup costs no query.

---

# Addendum 5 — Replies by quotation

Extends section 7, section 9, and section 14 property 2. Corrects Addendum 3 section 8 in part.

Section 14 property 2 states that a reply does not return to the first server. Addendum 3 section 8 states that the absence of a reply path is what permits the absence of provenance. The first statement is a limit of the original transfer model, not a necessary property. The second holds only for a reply path carried **in the protocol**.

This addendum defines a reply mechanism that carries no new field, adds no protocol state, and touches no anonymity property. A reply travels as an ordinary published item. Its relationship to its parent is carried in its own body, and every server resolves that relationship locally from a value it already computes.

The mechanism is optional. A server that does not want it sets one flag and behaves exactly as the base specification describes.

---

## 1. The principle

`content_hash` is computed from the item fields only, and every server computes the same value for the same item. It is already the duplicate key of section 6.

A reply quotes the fields of its parent in its own body. Those fields are exactly the fields that already travelled with the parent. A receiving server therefore recomputes the parent hash from the quote and finds the parent in its own store with one lookup.

**The identity of the parent is its content. Nothing else is needed, and nothing else travels.**

The quote block is also the display of the parent for a reader whose server does not hold it, so the mechanism costs nothing beyond what a quoted reply already contains.

---

## 2. What a reply is

A member reads an item, local or received, and writes a reply. The server writes an ordinary local row:

| Field | Value |
|---|---|
| `handle` | The member on this server |
| `topic` | The topic of the parent |
| `subject` | The subject of the parent, or a local convention |
| `origin_time` | The current time at this server |
| `is_remote` | 0 |
| `body` | The quote block, then the reply text |
| `content_hash` | Computed from the fields above, as section 6 |
| `quoted_hash` | The parent hash, computed from the quote block, section 5 |

Publication follows section 5 without change. The row takes a `pub_seq` when published, transfers to every pair whose `out` list matches the topic, and relays under Addendum 1 in the ordinary way.

**A reply is a post.** It is filtered, hashed, relayed, trimmed, and loop-controlled by the existing rules with no exception.

---

## 3. The quote block

The block is the first part of the body. Each line starts with `>` and a space. The first line is the header, in this order and with this separator:

```
> handle @ decimal(origin_time) | topic | subject
> The body of the parent.
> Further lines of the parent body.
```

An empty line ends the block. The reply text follows.

The fields are the five inputs to `content_hash`, in the order section 6 gives them. A parser reads the header line for the first three, and the remaining quoted lines, with the `> ` prefix removed and the final newline discarded, for the body.

| Field | Source in the block |
|---|---|
| `handle` | Header, before `@` |
| `origin_time` | Header, between `@` and the first `|` |
| `topic` | Header, between the first and second `|` |
| `subject` | Header, after the second `|` |
| `body` | Every line after the header, prefix removed |

A quoted body above the field limit of section 12 is truncated by the writer. A truncated quote does not produce the parent hash and the reply is treated as an orphan by section 6. A server that truncates should mark the quote as partial for the reader.

The block is text. A reader of a server that does not implement this addendum sees a quoted reply and loses nothing.

---

## 4. New columns

### 4.1 On `posts`

| Column | Type | Default | Function |
|---|---|---|---|
| `quoted_hash` | BLOB | NULL | The `content_hash` of the quoted parent, section 5 |
| `parent_id` | INTEGER | NULL | The local row of the parent, when resolved |

Both are local. Neither travels. `quoted_hash` is derived from the body at write and at receipt, so a server that adds the column later can populate it by re-reading the bodies it holds.

### 4.2 On `peers`

| Column | Type | Default | Function |
|---|---|---|---|
| `accept_replies` | INTEGER | 1 | 0 discards every received item that carries a quote block |

### 4.3 Server settings

| Setting | Default | Function |
|---|---|---|
| `thread_replies` | 1 | 0 stores an accepted reply as an ordinary item and computes no hash |
| `orphan_replies` | `keep` | `keep` stores a reply with no resident parent at top level. `drop` discards it. |

### 4.4 Index

```sql
CREATE INDEX idx_posts_quoted ON posts(quoted_hash) WHERE quoted_hash IS NOT NULL;
```

---

## 5. Resolution at receipt

Inserted between step 5 and step 6 of section 9, inside the same transaction.

1. **Accept.** When `peers.accept_replies` is 0 and the item carries a quote block, discard the item and move the cursor.
2. **Thread.** When `thread_replies` is 0, store the item as an ordinary row with `quoted_hash` NULL and `parent_id` NULL. Stop.
3. **Parse.** Read the quote block. No block gives `quoted_hash` NULL and an ordinary top-level row. Stop.
4. **Hash.** Compute `quoted_hash` from the parsed fields, by the section 6 rule, using the topic **as quoted** and before any `topic_prefix` of this pair.
5. **Attach.** `SELECT id FROM posts WHERE content_hash = ?`. A row found sets `parent_id`. 
6. **Orphan.** No row found, and `orphan_replies` is `keep`: store with `parent_id` NULL. `orphan_replies` is `drop`: discard the item and move the cursor.

The same procedure runs when a member writes a reply locally, at steps 3 to 5.

---

## 6. Late attachment

Order is not guaranteed across pairs, so a reply may arrive before its parent.

On every insert of any item, after `content_hash` is computed, the server runs:

```sql
UPDATE posts
   SET parent_id = (SELECT id FROM posts WHERE content_hash = ?)
 WHERE parent_id IS NULL
   AND quoted_hash = ?;
```

Both parameters are the `content_hash` of the item being inserted. An orphan binds the moment its parent arrives.

A thread therefore assembles out of order. A server that receives a conversation in reverse ends with the same tree as a server that received it in sequence.

---

## 7. The return path

Server A publishes a post. Server B pulls it and a member of B replies. The reply is a published item of B under the topic of the parent.

A pair that pulls from B carries the reply. When A pulls from B, A computes the quoted hash, finds its own row, and attaches the reply to its own thread.

**A reply returns when the pair runs in both directions.** No push exists, no address is held in the item, and no server learns anything it did not already hold.

The reply also reaches every other server that accepts the topic and holds the parent, by ordinary relay, without passing through A. Server C attaches the reply whether or not C holds any link to A.

---

## 8. Convergence

Two servers holding one parent may hold different reply sets. Both trees are correct for their position.

| Condition | Result |
|---|---|
| Both servers accept the topic and are connected within its subgraph | The trees converge within a few sync intervals |
| One server has a narrower `in` list | It holds the subset that matched |
| One server has a shallower window | It holds the subset still resident |
| The subgraph is partitioned | Two trees, neither aware of the other |

There is no thread state, no agreement, and no reconciliation. A tree is the local result of what arrived.

---

## 9. The window bounds the mechanism

A reply attaches only to a parent the server still holds. Past the window the parent row is gone, the lookup fails, and section 5 step 6 decides the outcome.

This bounds the mechanism without any rule of its own:

- The search set is the resident window and never more.
- A thread ages out whole, because the replies and the parent trim on the same schedule.
- No structure accumulates. There is no thread table, no orphan queue, and no tail.

Depth of threading follows depth of history, which follows narrowness of the `in` list. A server dedicated to one topic holds long threads. A broad hub holds short ones. The lever is the one the administrator already has.

---

## 10. Cost

For each received item, when `thread_replies` is 1:

| Operation | Cost |
|---|---|
| Parse the quote block | One pass over the body |
| Compute `quoted_hash` | One SHA-256, a few microseconds at 1 KB |
| Look up the parent | One index probe against the resident window |
| Late attachment | One indexed update for each insert |

The store is resident at every window size the design uses, so no read reaches the disk. The cost is independent of the window count and of the network size.

`thread_replies` 0 removes all of it. This is the setting for an administrator who wants the base behaviour and no additional work per item.

---

## 11. Display

| State | Presentation |
|---|---|
| `parent_id` set | Collapse the quote block. Show the reply threaded under the parent. |
| `parent_id` NULL, quote present | Show the quote block. The item is self-contained: the reader sees the original handle, time, and text, then the reply. |
| No quote | An ordinary item. |

The second state is not a failure state. It is a quoted reply, which is readable on its own.

A reader sees the handle and time of the parent, which they would see from the parent itself. Nothing about the server that wrote the parent is shown, because nothing about it is present.

---

## 12. Effect on the existing properties

| Property | Effect |
|---|---|
| Anonymity of the first server | None. The quote carries the item fields only, and those already travelled. |
| Provenance | None added. No path, no hop count, no server identity. |
| Loop control | None. A returning reply is rejected by the hash index like any item. |
| Filtering | None. A reply carries a topic and passes the `in` and `out` lists as a post. |
| Relay | None. Addendum 1 applies without change. |
| Locality of decisions | Held. Accept, thread, and orphan handling are per pair or per server. |
| Cursor and sequence | None. A reply takes a `pub_seq` as any published row. |
| `peer_seq_map` and `parent_seq` | Unchanged. They remain the mechanism for children sent in one batch with their parents by one server. Cross-server threading uses the hash and needs no map, because the hash is stable everywhere and `seq` is not. |

### 12.1 Corrects section 14 property 2

Property 2 is replaced by: a reply returns to the server of the parent when a pair runs in that direction and both servers accept the topic. A reply that does not return is a routing outcome, not a property of the design.

### 12.2 Corrects Addendum 3 section 8

The statement that the absence of a reply path permits the absence of provenance holds for a reply path in the protocol. It does not hold for a reply carried as content. The three decisions are separable, and this addendum separates them.

The consequence Addendum 3 draws — fifty servers producing fifty independent conversations — remains true where the subgraph is sparse or the windows are short, and becomes false where servers are well connected within a topic. Both are acceptable outcomes and neither requires any action.

---

## 13. Administrator decisions

All are set once, at link time or at configuration. No per-item step exists.

| Decision | Scope | Setting |
|---|---|---|
| Accept replies from this pair | Pair | `accept_replies` |
| Thread or flat | Server | `thread_replies` |
| Keep or drop an orphan | Server | `orphan_replies` |
| Send replies onward | Pair | `relay` and the `out` list, unchanged |

A reply is published by its author's server on its own terms. What a receiver does with it is the receiver's decision and is invisible to the sender. Two servers may reach different results for the same reply, and both are correct.

---

## 14. Limits

| Item | Value |
|---|---|
| Quote block, total | 4 KB |
| Quoted body within the block | 2 KB, truncated by the writer above this |
| Quote blocks in one body | 1, the first is parsed and any other is text |
| Threading depth for display | 8, deeper items shown at the last level |

A reply that quotes a reply is handled identically, because the quoted item is an item and its hash is computed the same way.

---

## 15. Order of work

| Stage | Content |
|---|---|
| 1 | The two columns, the index, the quote writer in the interface |
| 2 | The parser and the hash of the quoted fields |
| 3 | The lookup at receipt and the local write |
| 4 | The late attachment update |
| 5 | The three settings and the pair column |
| 6 | The threaded display and the orphan display |

Stages 1 to 3 give threading of local replies against received parents. Stage 4 makes it order independent. Stage 5 makes it optional. A server that stops at stage 3 is correct and behaves well.

---

# Addendum 6 — The `reply_to` reference

Adds a field. Corrects section 7.3, section 14 item 2, and appendix B.

Section 14 states that a reply does not return to the first server. That stays true. This addendum does not change it and adds no return path.

## 1. What this is

A post may carry a `reply_to` value that names another post. The value is text and nothing resolves it. The two servers need no pair, no path, and no knowledge of each other.

A post with a `reply_to` value is an ordinary post. It is not a comment, it does not attach to anything, and it appears in the topic list in its own right.

**The meaning is: this post responds to that post.** Nothing more. No delivery, no receipt, no notification, no confirmation that the named post exists, and no confirmation that its writer will ever read this one. The two writers find each other only because both posts carry the same topic and a reader of that topic sees both.

## 2. Format

```
handle:timestamp:subject
```

| Part | Content | Rule |
|---|---|---|
| `handle` | The handle of the writer of the named post | The handle character set, 24 characters at most |
| `timestamp` | The `origin_time` of the named post | Unix seconds, decimal, no sign |
| `subject` | The subject of the named post | The rest of the string, colons allowed |

The separator is a colon. The split takes the **first two** colons only, so a subject that holds a colon stays whole.

Total length: 300 characters at most. The three parts are the same three fields the reader already sees on the named post, so a member can write the value by hand, and the interface can build it with one action on a displayed post.

## 3. Why these three fields

They are the fields that travel unchanged to every server, from appendix B. `handle`, `origin_time`, and `subject` are identical on every copy of a post at every hop. A reference built from them means the same thing everywhere, without any global identity and without carrying provenance.

The `content_hash` of section 6 would be a stronger key, but it is local and never travels, so no member could write it and no reader could verify it. The three visible fields are the only shared vocabulary the design has.

## 4. Not unique

The reference does not identify one post. Two servers may hold members with the same handle, and two posts may share a handle, a second, and a subject.

This is accepted for the same reason as the handle blocks of addendum 3: the alternative is a global identity that carries the first server. The reference is a description that a reader interprets, not a key that a program resolves.

## 5. Storage and transfer

**New column on `posts`**

| Column | Type | Default | Function |
|---|---|---|---|
| `reply_to` | TEXT | NULL | The reference, section 2 |

**New field in the transfer record of section 7.3**

```json
"reply_to": "root:1754870400:The shadow war"
```

The value travels unchanged at every hop, like `handle` and `origin_time`. A prefix from `topic_prefix` does not touch it. Null when absent.

**The hash of section 6 does not include it.** The value is:

    SHA-256( handle || 0x00 || topic || 0x00 || subject || 0x00 || body || 0x00 || decimal(origin_time) )

unchanged. Two posts identical except for `reply_to` collide and one is discarded. This is the correct behaviour, because such a pair is the same post published twice with a reference added on the second attempt.

## 6. Validation

A server accepts a `reply_to` value when:

1. The length is 300 characters at most.
2. Two colons at least are present.
3. The first part is a valid handle, 1 to 24 characters of the handle set.
4. The second part is decimal digits only, 1 to 20 of them.
5. The third part is not empty.

A value that fails any test is discarded and the post is stored with `reply_to` NULL. **The post is not rejected.** A malformed reference is a cosmetic fault, not a reason to lose the text.

No server checks that the named post exists. No server can.

## 7. Display and search

The interface shows the value above the body as plain text, marked as a reference. It is not a link, because there is nothing to link to.

A reader may search on it:

```sql
SELECT * FROM posts WHERE reply_to = ? ORDER BY created DESC;
```

An index on `reply_to` is optional. It is useful on a hub, where the store is large enough that a scan costs something.

A useful local view: for a displayed post, search for posts whose `reply_to` matches the reference built from that post's own three fields. This shows the responses that reached **this** server. It is not the set of responses that exist. There is no way to obtain that set, and the interface should not suggest that the list is complete.

## 8. How two writers find each other

1. A writes a post on server 1, topic `games.turnbased`.
2. The post travels through pairs and relays and reaches server 2, where B reads it.
3. B writes a new post on server 2, topic `games.turnbased`, with `reply_to` naming A's post.
4. B's post travels and may reach server 1, where A reads it.

Step 4 is not guaranteed. It happens when a path exists from server 2 back to server 1 and every pair on that path carries the topic. It may take any amount of time or never occur.

**The topic is what makes it possible.** A response written into a different topic travels a different route and will not meet the same readers. This is the one convention the two writers must share, and it is a convention rather than a rule.

## 9. Properties

1. **No delivery guarantee.** Nothing confirms that the named writer received the response.
2. **No receipt.** Nothing reports back to the responder.
3. **No thread.** The two posts are separate rows and separate items in the list. There is no parent, no child, and no `parent_seq`.
4. **No resolution.** No server looks up the named post, and a reference to a post that never existed is stored and shown like any other.
5. **No new metadata.** The three fields already travel, so nothing about the first server, the path, or the hop count is added.
6. **The topic is the meeting place.** Two posts meet only where both are carried.

## 10. Limits

| Item | Value |
|---|---|
| `reply_to` length | 300 characters |
| Handle part | 24 characters |
| Timestamp part | 20 digits |
