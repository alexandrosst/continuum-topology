---
id: approve
title: "5. Approve it"
description: Read the approval code from the agent's own pod log, type it into Ikhnos, and watch the cluster become part of your estate.
---

<StepBar n={5} of={7} title="Approve it" />

# Approve it

The agent is running and has asked to join, but nothing it says is trusted yet. You now confirm that **this** agent is the one you meant. Ikhnos does that with a code that only someone who can read the cluster can see.

## Why a code, and not just the token

The token in the command proves that someone was allowed to install an agent. It does not prove that the agent that arrived is the one you meant. If two clusters were joining at once, or a token had leaked, the wrong request could be approved by mistake.

So there is a second secret that never travels with the first. On first contact the agent makes up a short code and prints it **only in its own pod log**: not to the server, not to the interface. The server keeps only a hash of it. Typing the code back is the proof that you, or someone with access to that cluster, saw it.

<Figure name="EnrollmentSequence" caption="Two secrets that never travel together. After step 8, every connection is mutual TLS, and the cluster needed no inbound port at any point." />

## Read the code

Run this against the cluster where you installed the agent:

```bash
kubectl -n ikhnos-system logs deploy/ikhnos-agent | grep -i 'approval code' | grep -oE '[A-Z0-9]{4}-[A-Z0-9]{4}'
```

<See>
One line with a code shaped like `K7QM-4TXD`, and nothing else.
</See>

## Type it in

In Ikhnos, the checklist now says an agent is waiting and offers **Review approval**. It takes you to the **Agents** page, where an **approval card** says the agent wants to connect. The agent's row shows the status **Pending** and the connection **Waiting for approval**.

The card shows what the agent says about itself, so you can check it against what you expect: its Kubernetes version, the address it connected from, its own version, and how long the request stays open. It also shows the **cluster fingerprint** the agent reports (the UID of the cluster's `kube-system` namespace), and the **access** it will get, which you can lower here before approving, but not raise above the ceiling.

Type the code into the **Approval code** field. Case, dashes and spaces do not matter. Press **Approve**. If this is not an agent you recognise, **Reject** is next to it.

:::caution[Five wrong codes end the request]
Five wrong attempts reject the request permanently, and the card shows how many are left. If that happens, connect the cluster again with a fresh token. Every enrollment needs a code; there is no setting that skips it.
:::

## What happens next

The server issues the agent a certificate from its own certificate authority. The agent switches to mutual TLS, opens its long-lived stream, and sends its first picture of the cluster. You will see the wizard move on to **Discovering** and then **Connected**.

<See>
On the **Agents** page, the agent shows **Approved** and **Connected**, with a recent heartbeat and a certificate expiry about a day away. The checklist on Topology disappears, because it has done its job. Nodes, namespaces and services start appearing within moments.
</See>

The certificate lasts 24 hours and renews by itself at the halfway point, so you do not have to remember anything. If the agent is ever offline for less than seven days it rejoins by itself, with no new token.

## If it did not work

| You see | Usually means | What to do |
|---|---|---|
| The log has no code | The agent has not reached the server yet, or an older pod was replaced. | Check the agent's log for the reason, and the previous step's table. |
| The code is rejected | A typo, or the code is from an earlier pod. | Read it again from the current pod. The card counts your attempts. |
| The request expired | The server waits up to 24 hours for approval, then the agent asks again by itself with a new code. | Read the new code and approve it. |

[Next: explore →](./explore.md)
