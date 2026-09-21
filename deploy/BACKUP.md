# Backup, restore and upgrades

What to keep, how to take a consistent copy, how to put it back, and what an upgrade does to your data. The
volume-snapshot and cold-copy options for a Kubernetes install are described in the
[chart README](README.md#backup-and-restore); this page covers the server's own `backup` and `restore`
commands, which work on any install (a container, a VM, a laptop) and take a consistent copy **while the server
runs**.

## What holds state

| Where | What | Lose it and… |
|---|---|---|
| `<data-dir>/continuum.db` (+ `-wal`, `-shm`) | SQLite: accounts, sessions, organisations, enrolment tokens, agents and their approvals, settings, **the workspace** (what people declared), **tombstones**, the node identity registry, the model version, audit | everything people entered is gone. This is the file to protect. |
| `<data-dir>/pki/` (`ca.crt`, `ca.key`) | the certificate authority every agent pinned at enrolment | **every agent is orphaned**: they refuse a server with a different CA and each cluster must be enrolled again |
| Neo4j (optional) | topology history, events, audit and workspace revisions | you lose history and the ability to look at the past; nothing else. Not the trust root. |
| what agents observe | clusters, nodes, workloads, traffic | nothing: it is **not stored in a backup and not needed in one**. Agents send a full picture when they reconnect. |

Because what agents observe is kept apart from what people declare (see
[twin-design.md](../backend/docs/twin-design.md)), a backup is small, and a restored server does not bring back
a stale picture of the estate: it shows the last-known tombstones and the declared workspace, and the live state
returns within a heartbeat or two of the agents reconnecting.

## Take a backup

```console
server backup --data-dir /data --out /backups/continuum-2026-09-21.tar.gz
```

* It is safe **while the server is running**. The database is copied with SQLite's `VACUUM INTO`, which is one
  read transaction: a single moment in time, including what is still in the write-ahead log. Copying
  `continuum.db` with `cp` is not safe for that reason.
* It never migrates or otherwise touches the source database.
* The archive is a `.tar.gz` holding `manifest.json` (format version, schema version, a SHA-256 of every file),
  `continuum.db` and `pki/*`. It refuses to overwrite an existing file and writes it with mode `0600`.
* **The archive contains the CA private key.** Treat it like the data directory itself: encrypt it (`age`, `gpg`)
  before it leaves the machine, and keep it away from the systems it protects.
* `--out -` writes the archive to standard output instead, for a container that has no shell or `tar`.

In Kubernetes (the image's entry point is the server, so give the command explicitly):

```console
kubectl -n continuum exec deploy/continuum-server -- /server backup --data-dir /data --out - > continuum-$(date +%F).tar.gz
```

The command needs somewhere to put its working copy of the database: it uses a private directory inside the data
directory (the chart's writable volume) and removes it afterwards.

**Neo4j** has no online backup in the Community edition. For a fully consistent copy stop it and run
`neo4j-admin database dump`; a volume snapshot is crash-consistent and enough for history. See the chart README.

**How often.** After anything you would not want to re-enter (a big edit of the workspace), before every
upgrade, and on a schedule (daily is plenty: a backup is a few hundred kilobytes for most installs). A
CronJob that runs the `kubectl exec` line above and copies the file off the cluster is enough.

## Restore

Restore into an **empty data directory**, with the server **stopped**. The server must not be running: it would
overwrite the files.

```console
server restore --data-dir /data --from /backups/continuum-2026-09-21.tar.gz
```

What it does, in this order, and stops at the first thing that is wrong:

1. Unpacks into a staging directory. Only the expected names are accepted (`manifest.json`, `continuum.db`,
   `pki/<file>`); anything else, or a path that leads elsewhere, is refused.
2. Checks the format: an archive written by a **newer** Continuum is refused ("Restore it with that version").
3. Checks every file against the manifest's SHA-256, so a truncated or edited archive is refused.
4. Checks the database: SQLite's integrity check must pass, and its schema version must be one this server
   understands. A database from a **newer** server is refused, untouched.
5. Requires the CA key to be present.
6. Puts the files in place. If the data directory already holds a database or a CA it refuses unless you pass
   `--force`, and then it **moves them aside** into `<data-dir>/pre-restore-<time>/` rather than deleting them.

Then start the server. It logs `continuum server started` with the same `ca_pin` as before (if it differs,
agents will not connect). Agents reconnect on their own and send a fresh picture; no agent is enrolled again.
The restored database is older than what you had, so anything decided after the backup (approvals, workspace
saves, new organisations) is missing: that is the price of restoring, and why the schedule matters.

In Kubernetes, restore into the volume from a pod that is not the server, with the server scaled to zero:

```console
kubectl -n continuum scale deployment/continuum-server --replicas=0
kubectl -n continuum run continuum-restore -i --rm --restart=Never \
  --image=continuum/server:TAG \
  --overrides='{"spec":{"securityContext":{"runAsUser":65532,"runAsGroup":65532,"fsGroup":65532},
    "containers":[{"name":"continuum-restore","image":"continuum/server:TAG","stdin":true,
      "command":["/server","restore","--data-dir","/data","--from","-","--force"],
      "volumeMounts":[{"name":"data","mountPath":"/data"}]}],
    "volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"continuum-server-data"}}]}}' \
  < continuum-2026-09-21.tar.gz
kubectl -n continuum scale deployment/continuum-server --replicas=1
```

(Use your image and PVC name. `--from -` reads the archive from standard input.) Drop `--force` for a volume you
know is empty.

## Try it before you need it

A backup you have not restored is a hope, not a backup. On any machine:

```console
server restore --data-dir /tmp/continuum-check --from continuum-2026-09-21.tar.gz
server --data-dir /tmp/continuum-check --admin-listen 127.0.0.1:18080 --agent-listen 127.0.0.1:18443 --agent-address 127.0.0.1:18443
```

Sign in, look at the workspace, stop it and delete the directory.

## Versions, upgrades and rollbacks

Three things are versioned, and none of them is allowed to go backwards.

| What | Rule |
|---|---|
| **Database schema** (`PRAGMA user_version`, now 4) | On start the server migrates an older database forward, once, in a transaction. A database written by a **newer** server is refused at start-up with a message that names both versions; nothing is touched. Migrations are forward-only. |
| **Workspace document** (`schemaVersion`, now 4) | An older document is rewritten into the current declared-only form, and the next person to open it is told what changed. A document from a **newer** version is refused on save and on import, and nothing is changed. Format 4 holds no discovered records; an older file that does is stripped on load (with a note), and the overrides you made on discovered records are kept. |
| **Backup archive** (`manifest.json` `format`, now 1) | An archive from a newer Continuum is refused by `restore`. |

The consequence for upgrades:

1. **Take a backup before every upgrade.** It is the rollback: a migrated database cannot be opened by the older
   server (it refuses, on purpose, instead of risking damage).
2. Upgrade. The server migrates on start and logs it.
3. To roll back, stop the new server, `server restore --force` the backup with the **older** binary, start the
   older server. Do not point an older server at a migrated data directory.

Restoring a backup **made by an older version** into a newer server is normal: it is migrated on start like any
older database.
