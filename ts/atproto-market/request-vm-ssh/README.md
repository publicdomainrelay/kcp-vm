# request-vm-ssh

The requester DenoPod of a MarketRequester application. It never runs on its
own: `socialweb-computer run request` starts a fresh kcp for the invocation,
applies the MarketRequester root object, and the core renders this entry as the
`requester` DenoPod (`deploy/apps/market-requester/<profile>.values.yaml`
through the service template). Outside a DenoPod it exits 2.

What it does, in its own kcp:

- signs in through the MarketIdentity object `requester` (the core creates it
  from `spec.identity`; the CLI renders its QR, URL and association prompts);
- reads the ComputeContract object `contract` and carries out the RFP flow for
  it (`requestCompute`): RFP, bids, accept, receipt, the guest's onNetwork and
  its ssh host keys, each step written to `status.contract` before it goes on;
- holds the contract until `spec.release` is set, then sends `vm.delete`
  (`releaseCompute`).

A restart (a new process, or the same kcp started again from the invocation's
state dir by `run request release|resume <name>`) resumes from
`status.contract`. The repo key, the sign-in session, the secrets bundle and
each contract's secrets-server key live only in the application's own OpenBao
(the OpenBaoServer `requester`), never in a kcp object.

The guest is reached only over its iroh ticket: the CLI runs
`ssh -o ProxyCommand="dumbpipe connect <ticket>"` with
`StrictHostKeyChecking=yes` against the host keys the guest reported.
