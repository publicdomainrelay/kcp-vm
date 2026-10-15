# request-vm-ssh

The requester controller DenoPod. It never runs on its own: an application's
core renders it as the `requester` DenoPod (`deploy/apps/market-requester/` for
a MarketRequester application, `deploy/apps/gateway/requester.*.values.yaml`
for a Gateway application) through the service template. Outside a DenoPod it
exits 2.

It is a controller over every ComputeContract object of its workspace. For each
contract it:

- carries out the RFP flow (`requestCompute`): RFP, bids, accept, receipt, the
  guest's onNetwork and its ssh host keys, each step written to
  `status.contract` before it goes on, with the bids it saw and why it chose
  one in `status.bids` and `status.chosen`;
- holds the contract until `spec.release` is set or the object is deleted, then
  sends `vm.delete` (`releaseCompute`);
- admits at most `--max-concurrent` contracts at once and `--per-account-max`
  of one account; the others wait `Pending`.

Which identity carries a contract depends on the application:

- a MarketRequester application (`socialweb-computer run request`) has one
  identity, the MarketIdentity `requester` (the core creates it from
  `spec.identity`; the CLI renders its QR, URL and association prompts), and
  one contract, `contract`;
- a Gateway application sets `--account-custody-mount`: it has no
  identity of its own and carries the contracts of the accounts named by
  `spec.ownerDid`; it acts for each account through the atproto auth mount of its
  OpenBao, which signs every DPoP proof and holds the session. The web app writes one ComputeContract per NodeClaim
  (`spec.nodeJoin` names the cluster and the pinned meshd build; `spec.secretsRef`
  names the join bundle the web app wrote into its own mount) and maps the
  contract's status back; this controller holds no session, the requester key
  and each contract's secrets-server key.

A restart (a new process, or the same kcp started again from the invocation's
state dir) resumes every contract from `status.contract`. The repo key, the
secrets-server keys and (for a MarketRequester) the sign-in session live only
in the application's own OpenBao, never in a kcp object.

The guest is reached only over its iroh ticket: the CLI runs
`ssh -o ProxyCommand="dumbpipe connect <ticket>"` with
`StrictHostKeyChecking=yes` against the host keys the guest reported.
