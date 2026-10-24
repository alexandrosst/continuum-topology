---
id: sign-in
title: "3. Sign in"
description: Read the one-time admin password from the server's log, open the interface, and choose a password of your own.
---

<StepBar n={3} of={7} title="Sign in" />

# Sign in

The server has just started on an empty database, so it has one account, `admin`, with a password generated for you. It prints that password once, in its log, the first time it starts.

## Get the first password

```bash
kubectl -n ikhnos logs deployment/ikhnos-server | grep -A2 'First start'
```

<See>
A line that says `First start: sign in as admin with the one-time password`, followed by the password, and a note that you will be asked to choose a new one straight away.
</See>

Copy the password. It works once, for one purpose: to get you to the screen where you choose your own.

## Open the interface

The interface is not exposed outside the cluster by default, which is the safe default. Reach it with a tunnel from your machine:

```bash
kubectl -n ikhnos port-forward service/ikhnos-server 8080:8080
```

Leave that running, and open **https://localhost:8080** in a browser.

Your browser will warn you that the certificate is not trusted. That is expected: the server generated a certificate for itself, because it refuses to serve a sign-in page without TLS, and nothing has told it a proxy is doing that job. Click through the warning for this first try.

:::tip[Skip the warning]
`kubectl port-forward` already travels over the encrypted connection to your cluster's API server, so plain HTTP inside it is just as safe. Add `--set admin.behindTlsProxy=true` to the helm command from step 2 (with `helm upgrade --reset-then-reuse-values`), and use `http://localhost:8080` instead. Do this only for a tunnel to your own machine.
:::

## The sign-in page

<Shot src="journey/signin" alt="The Ikhnos sign-in page with a username and password field" caption="The sign-in page. Type admin and the password from the log. 'Use this browser without a server' runs the interface on a built-in sample estate, which is a good way to look around without connecting anything." />

Type `admin` and the password, and press **Sign in**.

## Choose your own password

<Shot src="journey/new-password" alt="The Choose a new password screen with its rules listed" caption="Because the first password was generated for you, the server will not let you go further until you replace it." />

The rules are listed on the page as you type: 12 to 128 characters, not the same as your username, not one character repeated, and at least one number and one symbol. A few random words with a number and a symbol work well, and are easy to remember.

## Your empty estate

<Shot src="journey/first-run" alt="The empty Topology page with a Getting started checklist showing four steps, none done" caption="The empty state. The checklist is worked out from what the server holds, never from what anyone clicked, so it is true after a reload, from another browser, and when a colleague did a step." />

<See>
The **Topology** page, empty, with a **Getting started** list of four steps and a prompt to connect a cluster. Along the left, the menu groups: Explore, Infrastructure, Telemetry and Manage. You may also see a gentle reminder to add a second sign-in step. It is worth doing at some point; it is not needed now.
</See>

You are in. The rest of this journey happens from this page.

[Next: connect a cluster →](./connect-a-cluster.md)
