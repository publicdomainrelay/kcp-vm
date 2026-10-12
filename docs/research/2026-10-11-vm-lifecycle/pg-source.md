# pg-source: where the pinned PostgreSQL 18 comes from (0006 M0-PG-SOURCE)

Date: 2026-10-11 (measured on hat-8, Fedora 44, glibc 2.43, x86_64, as uid 1000).
Consumers: the gateway kcp store (M2-KCP-STORE, kine -> postgres over a unix socket) and every tenant
cluster store (M2-POSTGRES). Dev and bidder kcp stores are kine-sqlite and need none of this.

## 1. Decision order (user, 2026-10-11, final)

1. **Official binaries first.** The PostgreSQL Global Development Group publishes no generic Linux
   binary tarball; its official binaries are the PGDG packages (apt.postgresql.org debs,
   yum.postgresql.org rpms). Use them if they can be extracted without root and run relocatably as
   a non-root user.
2. **Build from the official source tarball** (ftp.postgresql.org/pub/source) only if (1) cannot be
   made to work, with a build-host script `scripts/build-postgres.sh`. This is the fallback.
3. **Third-party repackagers are out**: zonky embedded-postgres-binaries and
   theseus-rs/postgresql-binaries are not used. Section 6 keeps the facts measured before that
   ruling, for the record only.

## 2. Choice

**PGDG `jammy-pgdg` debs for PostgreSQL 18.6 (`18.6-1.pgdg22.04+2`), amd64 and arm64, plus their
Ubuntu 22.04 (jammy) shared-library dependency closure, extracted without root into one
relocatable tree.** PGDG works on hat-8 and on every distro image tried, so the fallback was not
needed. A source build was prototyped and measured before the user's final order (section 5). Its
script is not committed.

Why jammy and not bookworm, noble or the EL rpms: PGDG binaries link the host distro's ICU,
OpenLDAP and OpenSSL by soname. The oldest PGDG target at or below the plan's floor (Ubuntu 22.04)
is jammy: max GLIBC_2.34 in the PG binaries and GLIBC_2.35 across the bundled libraries. EL8 links
OpenSSL 1.1, and EL9 links ICU 67. Both have the same soname problem as jammy, and neither lowers
the floor.

### What "relocatable" means for the PGDG tree (measured)

- **Layout must be kept.** PGDG compiles in `bindir=/usr/lib/postgresql/18/bin`,
  `sharedir=/usr/share/postgresql/18` and `pkglibdir=/usr/lib/postgresql/18/lib`. At startup
  PostgreSQL resolves share and pkglib relative to the running binary (`make_relative_path`) only
  while the tail `lib/postgresql/18/bin` of the binary's path matches. So the tree is kept as
  `<any-root>/usr/lib/postgresql/18/{bin,lib}` plus `<any-root>/usr/share/postgresql/18`, where
  `<any-root>` can be any path. That was proven from `.../arbitrary2/x y/z/...` (a path containing a
  space) on hat-8, and from `/opt/any/where/...` read-only in containers. Flattening to
  `bin/ lib/ share/` breaks it.
- **No RUNPATH in PGDG binaries.** `libpq.so.5` (from the PGDG libpq5 deb) and the jammy libraries
  sit in `<any-root>/usr/lib/sys/`, and are found only through
  `LD_LIBRARY_PATH=<any-root>/usr/lib/sys`. That variable must be set on the postgres, initdb and
  pg_dump exec ONLY, never in the core's own environment. Measured: hat-8's `grep`, spawned under
  that variable, loaded jammy's libpcre2 and warned
  `libpcre2-8.so.0: no version information available`. `pg_ctl` starts postgres through `/bin/sh`,
  which then inherits the variable. That is one more reason the units exec `postgres` directly
  (M2-POSTGRES, M2-KCP-STORE already do), not `pg_ctl`.
- **Host requirements left:** glibc >= 2.35 (`libc.so.6 libm.so.6 libresolv.so.2
  ld-linux-*.so`) and **host tzdata**. PGDG is built `--with-system-tzdata=/usr/share/zoneinfo`,
  an absolute path that is not relocatable. On `ubuntu:22.04` (no tzdata in the image) the server
  started with `TimeZone=UTC0`, and `SET timezone='UTC'` failed with
  `invalid value for parameter "TimeZone"`. On every image with `/usr/share/zoneinfo` it worked.
  check-host (M1) must require `/usr/share/zoneinfo/UTC` on gateway hosts.
- **Non-root:** every run was as uid 1000 on hat-8 and as uid 65534 in the container stand-ins.
  initdb refuses root anyway.
- **Port 0:** postgres refuses it in every build:
  `FATAL: 0 is outside the valid range for parameter "port" (1 .. 65535)` (the GUC minimum is 1).
  This shapes the two stores differently:
  - The gateway kcp store opens no TCP port at all: `listen_addresses=''` plus a unix socket
    directory. M2-KCP-STORE already specifies this.
  - Tenant stores that need TCP must lease a port (M2-POSTGRES "0 = leased"). The bound port is
    read back from `ss -ltnp` (matched on the postmaster pid) and from line 4 of
    `postmaster.pid`.
  - kine itself takes `--listen-address 127.0.0.1:0` and announces the bound port.
- **Collation:** initdb with `--locale-provider=builtin --builtin-locale=C.UTF-8 -E UTF8`, which
  gives `datlocprovider=b`, `datlocale=C.UTF-8` and `datcollversion=1`. The builtin provider is
  versioned with PostgreSQL itself, independent of glibc and ICU. kine's `name` column is
  `COLLATE "C"` anyway (kine v0.17.1 pkg/drivers/pgsql/pgsql.go:40). ICU 70.1 is still linked, and
  its `unicode` collation reports `collversion=153.112`. M2-POSTGRES records that value and treats
  a change as major-class only for databases that actually use ICU collations.
- **Security update churn:** 44 of the 47 pins are Ubuntu jammy packages (taken from jammy +
  jammy-updates + jammy-security, highest version). Every Ubuntu security update to libssl3,
  libxml2, krb5 and the rest changes the bundle's pins.

## 3. Verification steps (performed; M1-ASSETS repeats them in its assembly script)

1. PGDG key: `https://www.postgresql.org/media/keys/ACCC4CF8.asc`, imported into a fresh
   GNUPGHOME. Fingerprint **B97B 0AFC AA1A 47F0 44F2 44A0 7FCC 7D46 ACCC 4CF8**
   ("PostgreSQL Debian Repository", rsa4096, 2011-10-13).
2. `https://apt.postgresql.org/pub/repos/apt/dists/jammy-pgdg/InRelease` was verified against that
   key: Good signature, made 2026-10-11 09:30:45 UTC.
3. Ubuntu archive key **F6EC B376 2474 EDA9 D21B 7022 8719 20D1 991B C93C** ("Ubuntu Archive
   Automatic Signing Key (2018)") was fetched by full fingerprint from keyserver.ubuntu.com. It
   verified the `jammy`, `jammy-updates` and `jammy-security` InRelease files: archive.ubuntu.com
   for amd64, ports.ubuntu.com for arm64. All Good.
4. Each `main/binary-<arch>/Packages.gz` sha256 was checked against the SHA256 section of its
   signed InRelease.
5. Each .deb sha256 was checked against its `Packages` stanza (47 per arch, all matched; table
   below).
6. Extraction without root: `ar x <deb>` then `tar -xf data.tar.*` into a scratch root (no dpkg,
   no maintainer scripts). Then copy `usr/lib/postgresql/18/{bin,lib}`, `usr/share/postgresql/18`,
   and every `*.so*` from `lib/<triplet>` and `usr/lib/<triplet>` into `usr/lib/sys`. Drop
   `usr/share/postgresql/18/man` and `usr/lib/postgresql/18/lib/bitcode` (JIT bitcode; llvm-jit is
   a separate package and is not installed).
7. Repack: `tar --sort=name --owner=0 --group=0 --numeric-owner --mtime=@1786473511 | zstd -19`.
   The epoch is the newest mtime in postgresql-18.6.tar.bz2.

Helper scripts used: `apt_fetch.py`, `jammy_closure.py`, `pgrun.sh`, `pgmini.sh` and
`minisign_verify.py`, all in the session scratchpad. M1-ASSETS turns steps 1-7 into its own
script.

## 4. Pins

Repository bases: `https://apt.postgresql.org/pub/repos/apt/` (pgdg). For ubuntu,
`https://archive.ubuntu.com/ubuntu/` (amd64) and `https://ports.ubuntu.com/ubuntu-ports/` (arm64).
Filename = `<pool dir><package>_<version without epoch>_<arch>.deb`, for example
`https://apt.postgresql.org/pub/repos/apt/pool/main/p/postgresql-18/postgresql-18_18.6-1.pgdg22.04+2_amd64.deb`.

| package | version | repo | pool dir | sha256 amd64 | sha256 arm64 |
|---|---|---|---|---|---|
| postgresql-18 | 18.6-1.pgdg22.04+2 | pgdg | pool/main/p/postgresql-18/ | 2aab00b13babd3d9abc257bc5f6b00efa1f7c27cda071413f358d2e0cdb84420 | 8a379f0684b480b60e79657ac8178e8b00c835e7575e85ae746c091d336563d3 |
| postgresql-client-18 | 18.6-1.pgdg22.04+2 | pgdg | pool/main/p/postgresql-18/ | ad4e491adceaf1453162c62691473ace78ed81ab20f50d11b344a793d13ed4bd | 21f1b418621fc9c90785b18ec2d7fe4fd8ff111cc607fc3b3625464fc6e7886b |
| libpq5 | 18.6-1.pgdg22.04+2 | pgdg | pool/main/p/postgresql-18/ | 8af0b458834e219f3aa7c904fa1237ea484950486e3165a3a90204878e060530 | fc0d787c9da82bd0146e5e365989f3c92dbb3d73c3a32c62a2e4d56ab90933a6 |
| libaudit-common | 1:3.0.7-1ubuntu0.1 | ubuntu | pool/main/a/audit/ | 3e4e8586db5f57f431954a9125556fe6df86610592c0cf46ba1127fae8871e3f | 3e4e8586db5f57f431954a9125556fe6df86610592c0cf46ba1127fae8871e3f |
| libaudit1 | 1:3.0.7-1ubuntu0.1 | ubuntu | pool/main/a/audit/ | 00d7eb086dedc85bd5b9cbde650a1c71460499cb62e439acb761bc7bcb4eaaac | 963474b42258e8caa15eec1db61d8424fd6b7ef717ca4e371b3240506854b3fe |
| libcap-ng0 | 0.7.9-2.2build3 | ubuntu | pool/main/libc/libcap-ng/ | 124d0c8748a841f279e996298ef8aac69a249e294792f4b5e16dc00496f1d3ac | 884c8bc87da241abaa2b0ca80162486cf66eff67dfc545bea56622e1170e9ee5 |
| libcap2 | 1:2.44-1ubuntu0.22.04.3 | ubuntu | pool/main/libc/libcap2/ | c497efb7fa3cd19a7ff297d95c7c5d678063c80c9fc230c9d034a8ee02ca8446 | 7a8eb6549355a16f327ede574f12e6ebed983b52ab38ce4a618e6e9274b9d4f5 |
| libcom-err2 | 1.46.5-2ubuntu1.2 | ubuntu | pool/main/e/e2fsprogs/ | 552bc8169451cfdcf174a8da845b13ced0a55777149af5cf47491193161ed791 | 3a7b6a2b34c2171b7c8f9ddae58c93b6e416fcd9ac4fa8eb5a15a1a8f3f99afa |
| libdb5.3 | 5.3.28+dfsg1-0.8ubuntu3 | ubuntu | pool/main/d/db5.3/ | ce60e446c9cb0e94bb4b4d0fe4f969e772edec250deb186bacf955e4d196548b | e5b672aeaca690b3b7ee97b03dbc07ddb49be7b6ea0a770fa6707666ca1cef59 |
| libffi8 | 3.4.2-4 | ubuntu | pool/main/libf/libffi/ | b4f88c91fa6f4c942097be6abfc365fb133c5e147640168cbb7704fd855eac10 | a0cdf938e8da19bd2252c681f80268609a32d06496a1f8d0ebe1fa41debec697 |
| libgcc-s1 | 12.3.0-1ubuntu1~22.04.3 | ubuntu | pool/main/g/gcc-12/ | d383e642d83263147f7b4b69fb92cf1036e5246c6be3accf10b77f986684589a | 46626e1af6b159061e055f843654ee1f0f1e65fa280331ed47ee76ba1b1f6045 |
| libgcrypt20 | 1.9.4-3ubuntu3.3 | ubuntu | pool/main/libg/libgcrypt20/ | 8f16329a0aabf4900271a3687890721bbef307e60983acae5b92eeff12e799b3 | bd98a709641e7e61c8798bb84030886a96fd56424b053a6e9996a8391ce20597 |
| libgmp10 | 2:6.2.1+dfsg-3ubuntu1 | ubuntu | pool/main/g/gmp/ | d199a77e11701d4d620dd4f62e05740e3072dce893856ea3bbaa6ae9fa19fef1 | 32233a3bf7c8b19e86ef3629334f0bece716bb163605d12e0d3a443d57a401d7 |
| libgnutls30 | 3.7.3-4ubuntu1.9 | ubuntu | pool/main/g/gnutls28/ | 65e08f0b5d1e620cc5cc9478cd1035f83ea752a6fae23141afd2d111a8f6c555 | cce4bf33fa79446ddee42a992e569f12a57c1a6f8916a2e8349249be0f7ad655 |
| libgpg-error0 | 1.43-3 | ubuntu | pool/main/libg/libgpg-error/ | 1fbacdf9bd1e431cee874a697b339f6f925182bc79bba5a112b53669b33265c5 | ffa9029cdeb2786e0db1f614f3d2205f75f466a66b25e22aa89ecdf814491875 |
| libgssapi-krb5-2 | 1.19.2-2ubuntu0.10 | ubuntu | pool/main/k/krb5/ | 08e98a3dc74361cdb13c0d68bc73e0352552490546c1ad3ce5bff5426f6a2dfe | e1001bffd91dd306498ec1824a8330014399a0e0889a562069003726348d7f55 |
| libhogweed6 | 3.7.3-1build2 | ubuntu | pool/main/n/nettle/ | 2402ac51ebc760799b01f3fd4933126c5eb446e5b043832601fb589f5e50c363 | 844e5d093dd3982a85c134d47a9e22b129123fb817eb3eea96e936805b0f5170 |
| libicu70 | 70.1-2 | ubuntu | pool/main/i/icu/ | 58a154f6307289813da2276f900498ef536ae7c0522d2cf31a3c3c5cf62dfd9a | ac68372cf4a976e6a206858fd9b28c68e49d37d650b9b8653270038a6e7bc174 |
| libidn2-0 | 2.3.2-2build1 | ubuntu | pool/main/libi/libidn2/ | bc935e1871aa4a75d3f4ff549af32b7316aecfbea2200d85055878767b8398c8 | aeb29cc8deedc0710a97e44088ef0271dfe1197e77c0a0fc0c5602e14aeb938f |
| libk5crypto3 | 1.19.2-2ubuntu0.10 | ubuntu | pool/main/k/krb5/ | 40429c0099678fbc35045f0c63a10bc3490251d80cf1006095eaa0272c8d5e27 | e51d72b346813b57f26c09827f96eb856adb9b553814fbc61a5e8c8a25d292a3 |
| libkeyutils1 | 1.6.1-2ubuntu3 | ubuntu | pool/main/k/keyutils/ | 940daf78ee0229549b4eabc92ffd79dd038ed96a44e2e912305a23540cf22a0a | fc26b34c487ea5187585ee0f2c67c738b97e8bae434cca4b546d86743b9e6c80 |
| libkrb5-3 | 1.19.2-2ubuntu0.10 | ubuntu | pool/main/k/krb5/ | 0da3e0df7d6314615160a42f65ff345986783be02cd65a6ce797cfbacc7bcb0d | 8de066e35be457bf0c53c995f8baa270ad61566dcd892d0a7cc3c0438ff513b7 |
| libkrb5support0 | 1.19.2-2ubuntu0.10 | ubuntu | pool/main/k/krb5/ | 0406052ac2d267f05088ada6d36df61aaa338cbe8d5e13fb1d7f79da560817ed | c535d5ccf5ea99fb63d86b33082c5ed553a6aa8c7cbf3fed1ef0dac1d7b7dcd6 |
| libldap-2.5-0 | 2.5.20+dfsg-0ubuntu0.22.04.1 | ubuntu | pool/main/o/openldap/ | a3ee4ac116a2c50c9ddfe9de91c0781a50704244771474334340cdce02f5cd4f | 305ec2bf85ad8e8a6309b1a48e601f172503a267ca90d511ccbb098cdf009d6b |
| liblz4-1 | 1.9.3-2build2 | ubuntu | pool/main/l/lz4/ | ac9b54d0feb840345060c74fb687675c5e1eb2b195effafae38c5f9991041e98 | 3e2274893e0963c758f0d18ba80d033736909373247a7deb211e9ff590a09133 |
| liblzma5 | 5.2.5-2ubuntu1.1 | ubuntu | pool/main/x/xz-utils/ | af77f099e7fa95a31267023535832804382fa66572a6b38002187be6e15116cb | 97f4c7e360aa229d3bf0a5b56b204652ce4f1704536dc0256c0e2537b356f397 |
| libnettle8 | 3.7.3-1build2 | ubuntu | pool/main/n/nettle/ | 6da09e685e5587f8873f662b007de45f75424fb33d2c4a51837380e683d33c7e | bcd667d9a51c2474f7ffc52807ab4a24f8b877b6da1a6f5b75e50e48ec4d634a |
| libnuma1 | 2.0.14-3ubuntu2 | ubuntu | pool/main/n/numactl/ | 0721c89001fbbd1ada23e89da5d60e762763c1a7b3dc814a2e9a518480a8043d | ca67a774b20ed639361715ef0981d236f294ff2987579b3d18faede67c3a9152 |
| libp11-kit0 | 0.24.0-6ubuntu0.1 | ubuntu | pool/main/p/p11-kit/ | 9d949950970a062a81051a78ec4075b8dba608f063afaa5c8752a612d139a9b5 | 0cfad0790be90ccb56de67e6c58a67d73e4769028eb19a2c0d799fa19478c2ff |
| libpam0g | 1.4.0-11ubuntu2.8 | ubuntu | pool/main/p/pam/ | faaf4b6d1bfb33a7fc4e195448439c202869d67c8de17c4ecd29b7719e0ac4d6 | f61ae0342dfba2b6c6b61f3d46925371434a4719a251f7585f9baa1341fcc9e2 |
| libpcre2-8-0 | 10.39-3ubuntu0.1 | ubuntu | pool/main/p/pcre2/ | 01a5a74f3d305cad32fffffdc200c2a92fd8e5a01bff97de76ac598fb444d53e | b85beefd54108f1c34a89cb26688eb1b2eeaa099e4995091295a1d39f9c67a91 |
| libreadline8 | 8.1.2-1 | ubuntu | pool/main/r/readline/ | 51fd16f608efa710c987e4b071ffba0bec63a9ac8e55eddd5770f5e5252edc09 | 54291f2dd185d3a759227b31b7de4269e495a084fdad89e24a9cc4d0d20888a7 |
| libsasl2-2 | 2.1.27+dfsg2-3ubuntu1.2 | ubuntu | pool/main/c/cyrus-sasl2/ | 391cb48d7e601d5bb590da8d426750acf6c987241ba280a36227873b94f20cee | d4f60677a0024f2b888a563f1d396a1f43fe22097e01d0a7048695c3c1ef8e18 |
| libsasl2-modules-db | 2.1.27+dfsg2-3ubuntu1.2 | ubuntu | pool/main/c/cyrus-sasl2/ | 0e222c26d70c2cfd132c8de035bcf9673067b6cbfea0a7421dced7b6256ae774 | 438df2296b2f828ab05287fadbf1a29f2e3bc0d618807b01128829313be91895 |
| libselinux1 | 3.3-1build2 | ubuntu | pool/main/libs/libselinux/ | b96c6b40ee2388bd51341cb11c0f1d5bcca29b9180b6e3a77a06b881f2913f7e | 3a0726b2174a40fa28e74712867aa6e40d3d725543e8f668af9e9e59a28c817c |
| libssl3 | 3.0.2-0ubuntu1.30 | ubuntu | pool/main/o/openssl/ | d1b4636e32ac6d3b95d583ac05ac548b1aa248414bae1c51b14f54be0f8fd7bc | 1c4b8cc736eed8a0caafc385f854fa6852382221d84be4795f1d19a52b9fec53 |
| libstdc++6 | 12.3.0-1ubuntu1~22.04.3 | ubuntu | pool/main/g/gcc-12/ | 29ef3d289b272704b75141906c8f683db0f2bdb0942061366691333bfb289bdc | 59d244119e6129c7e9c0897491c0e210e925d15881b2dedcf8965ceb4fbf1aa3 |
| libsystemd0 | 249.11-0ubuntu3.22 | ubuntu | pool/main/s/systemd/ | 4fae4644bcfd86f891faae12cedbf30aca280cf76a034768aec3a03fe42ddc25 | 9805c8c136fd41d9bff355297fdf6f811529960908711f647e38e1fd39a198ec |
| libtasn1-6 | 4.18.0-4ubuntu0.2 | ubuntu | pool/main/libt/libtasn1-6/ | a0903b4fae3ac2548a22d8822382e6d23586e42196d83ff67e82df36cd39fc6c | d9f0ed3ca0cb86f6116bb59388aea92b021c05797b4fba7451a2fed594cb1e1e |
| libtinfo6 | 6.3-2ubuntu0.3 | ubuntu | pool/main/n/ncurses/ | 3b8dff43a31168f5020928f2ab9a97324c5b47ae708311781f6bd01306c96214 | 971b5fd1cf8f7d68ba00f40d8eec6474fae6a6adbe19883aa42dd2b74c672e4a |
| libunistring2 | 1.0-1 | ubuntu | pool/main/libu/libunistring/ | 988fb59de6a0a313844277e8a6e45d3dc16f5ea75a603a46b16b936426f0ac65 | 59e4af5c1fac70d1fbc19f3c732331ace8449f1f7112887e7b56b8370bd8942b |
| liburing2 | 2.1-2build1 | ubuntu | pool/main/libu/liburing/ | f6e9bdf50c9683cd9a74ad92d51dde085f40baf0a5fcd8a56fc68425c1bd3c5f | a9221f0d47fd32819558f7a91677e97c72bd88646d38d369ffa70720ac49c27d |
| libuuid1 | 2.37.2-4ubuntu3.6 | ubuntu | pool/main/u/util-linux/ | 97eaa1090c2e7ddaad55d58f9d0d7907d64b70f89228405d68a7ce7d56a89d67 | 2478ccd8df956d8e5cf087bff1471bcd9991838be9c7999e5737d092034c492d |
| libxml2 | 2.9.13+dfsg-1ubuntu0.14 | ubuntu | pool/main/libx/libxml2/ | 16e395b2c52d6ab232053468febc69f0876956ec2fae357a941a2a8dab36574f | 1e85b731baabd51331d70a6392c652bc39fa2c231ff784555bd6a61631009387 |
| libxslt1.1 | 1.1.34-4ubuntu0.22.04.5 | ubuntu | pool/main/libx/libxslt/ | 5e50bc83e9365942cf63272d8a45ce71170aaf4a1a4e3c4b30f591f6194d77ef | dd533b9980918ca64531254ccbdfd5efc492bbef37c70f0b5559b3b8f306b626 |
| libzstd1 | 1.4.8+dfsg-3build1 | ubuntu | pool/main/libz/libzstd/ | ae7db00ce8b093e50c994518b90203544e063b4bc574836a048bb142b950b2c9 | 0102eb80384c2432c4be819663341a1c4e511d05f13ec1467643696531ec6491 |
| zlib1g | 1:1.2.11.dfsg-2ubuntu9.2 | ubuntu | pool/main/z/zlib/ | 9dc17e51a1be2d9ed63b7b84ef0e4e29c5abe6f1bc62cb03e7181483cce8a2f2 | cfd99d1ca41e710fdd3f68bfdc4d36dd24e14377656764f00b215b89cf6976e8 |

Debs per arch: 47 (3 PGDG + 44 Ubuntu); 30,428,248 bytes (amd64) and 29,440,882 bytes (arm64).
Index digests at pin time (signed InRelease of 2026-10-11): jammy-pgdg
`main/binary-amd64/Packages.gz` ca96279cfb8c49c2ac59dbdb083cb099223ad9e9f23313951eb8f715c5a1577c and
`main/binary-arm64/Packages.gz` 911dc4a3e960d5df5679e29fe7756c6d0eee1f9ffb79fbc0a5d8cd3735f8f32b.

Assembled trees (this spike's assembly; M1-ASSETS re-pins its own output):

| arch | raw bytes | zstd -19 bytes | sha256 of .tar.zst |
|---|---|---|---|
| amd64 | 84,342,619 (pg bin 15.8 M, pg lib 5.7 M, sys libs 61.0 M of which libicudata 29.5 M, share 1.9 M) | 25,654,278 | 467436b73669a7ca00ee3c5c44c468f892cabcf670dd529ebeeb99aeb63123fd |
| arm64 | 81,912,395 | 25,072,723 | 39847c32e8c7d71667ce33c5b625a96d0a3273cb0f1bcfc9f0c42545e4eb12b2 |

Size lever for M0-SIZE-BUDGET: ICU is 30 MB of the 84 MB, and the builtin provider does not use
it. PGDG links it, though, so it cannot be dropped without the fallback build.

## 5. Measurements on hat-8 (uid 1000, arbitrary path, kine v0.17.1 `/usr/local/bin/kine`)

Harness: initdb, pg_ctl on a unix socket, psql, kine against the socket, an etcd v3 put+get through
kine (go.etcd.io/etcd/client/v3 v3.6.8), pg_dump, stop, port 0 attempt, then a leased TCP port.
Commands, PGDG tree (`B=<root>/usr/lib/postgresql/18/bin`, `LD_LIBRARY_PATH=<root>/usr/lib/sys`):

```
$B/initdb -D $T/data -U postgres --auth=trust -E UTF8 --locale-provider=builtin --builtin-locale=C.UTF-8
$B/pg_ctl -D $T/data -l $T/pg.log -w -o "-c listen_addresses='' -c unix_socket_directories='$T/run' -p 5432" start
$B/psql -h $T/run -U postgres -d postgres -Atc 'select 1'
kine --endpoint "postgres://postgres@localhost/kine?host=$T/run&sslmode=disable" --listen-address 127.0.0.1:0
$B/pg_dump -h $T/run -U postgres -d kine -f $T/kine.sql
$B/pg_ctl -D $T/data -m fast -w stop
$B/postgres -D $T/data -c listen_addresses=127.0.0.1 -c unix_socket_directories='' -p 0
```

| step | PGDG bundle (choice) | source build (fallback prototype) |
|---|---|---|
| initdb | 0.338 s | 0.266 s |
| pg_ctl start, unix socket only (no TCP listener: 0) | 0.109 s | 0.103 s |
| first `SELECT 1` after start returns | 0.009 s (0.123 s from start command) | 0.005 s (0.113 s) |
| kine on the socket | announced and bound 127.0.0.1:37867 (port 0) | 127.0.0.1:37055 |
| etcd put+get through kine | ok, rev 3, 4.7 ms | ok, rev 3, 5.2 ms |
| pg_dump of the kine db | 0.030 s, 3,385 bytes, has `CREATE TABLE public.kine` | 0.027 s, 3,329 bytes |
| pg_ctl stop -m fast | 0.102 s | 0.102 s |
| `-p 0` | refused: `0 is outside the valid range for parameter "port" (1 .. 65535)` | same |
| leased TCP port | 33959 bound, read back from ss and postmaster.pid, `inet_server_port()=33959` | 38745, same |
| RSS, postmaster tree idle (sum of VmRSS) | 101,736 kB | 56,812 kB |
| RSS, postmaster tree with kine connected | 183,752 kB | 118,668 kB |
| RSS, kine | 36,696 kB | 36,872 kB |
| fresh data dir | 66.3 MB | 48.6 MB |

Distro stand-ins (container images, uid 65534, `--network none`, tree bind-mounted read-only at
`/opt/any/where/...`): initdb + pg_ctl on a unix socket + `SELECT 1` + pg_dump + stop all passed
for the PGDG bundle and for the source build on these images:

- `ubuntu:22.04@sha256:5ec03bb3441e8b0bf3b4f9cd4629a1ae763010dc3035bb8da3ae6cf026486401` (glibc 2.35, no tzdata)
- `ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55` (2.39, no tzdata)
- `debian:12-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587` (2.36)
- `debian:13-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f` (2.41)
- `fedora:44@sha256:43b29f65a41eb9c35e1cd5323e3bdf3b655c2357a9f4f1ff2f9c2798e5045d80` (2.43)

On the images without tzdata, PGDG ran with `TimeZone=UTC0` and refused `SET timezone='UTC'`. The
source build carries its own `share/timezone` and accepted `UTC` and `Europe/Berlin` everywhere.
arm64: the PGDG arm64 bundle passed the same test in `debian:12-slim` (linux/arm64) under hat-8's
qemu-aarch64 binfmt (`uname -m` = aarch64).

Without the bundle's libraries, the bare PGDG debs on hat-8 failed:
`libldap-2.5.so.0: cannot open shared object file`. libicu*.so.70 and libpq.so.5 were missing as
well. On minimal images, libnuma, liburing, libxml2, readline, krb5 and OpenSSL 3 were also
missing (debian:13 additionally lacked libgnutls30). That is why the bundle carries the whole
jammy closure.

## 6. Fallback: build from the official source tarball (status: prototyped, not committed)

Use only if the user rejects section 2's costs: the 44 Ubuntu pins, LD_LIBRARY_PATH, host tzdata,
25 MB zstd and 100 MB RSS. Measured prototype, copy kept at
`$TASK_STATE_DIR/build-postgres.sh.fallback-prototype`:

- Source: `https://ftp.postgresql.org/pub/source/v18.6/postgresql-18.6.tar.bz2`, sha256
  `555610c24d53e4316da5b7d3fc25c279d96856d5e0e23ee308c328c5fa881d9f` (matches the published
  `.sha256`; upstream publishes no PGP signature for source tarballs).
- Since PG 17 the tarball ships no generated parser, so the build host needs bison, flex and perl.
  The prototype builds pinned host tools with zig itself:
  - m4-1.4.21: f25c6ab5...ab66, GPG Good, Eric Blake 71C2CC22B1C4602927D2F3AAA7A16B4A2527436A
  - bison-3.8.2: 9bba0214...5bf2, GPG Good, Akim Demaille 7DF84374B1EE1F9764BBE25D0DDCAA3278D5264E
  - flex-2.6.4: e87aae03...e995, GPG Good, Will Estes 56C67868E93390AA1039AD1CE4B29C8D64885307
- Toolchain: zig 0.16.0, x86_64 sha256
  70e49664a74374b48b51e6f3fdfbf437f6395d42509050588bd49abe52ba3d00, minisign Good with
  `RWSGOq2NVecA2UPNdBUZykf1CCb147pkmdtYxgb3Ti+JO/wCYvhbAb/U`; aarch64 sha256
  ea4b09bfb22ec6f6c6ceac57ab63efb6b46e17ab08d21f69f3a48b38e1534f17. It targets
  `<arch>-linux-gnu.2.28`, so the glibc floor is 2.28 regardless of build host, and it
  cross-compiles arm64 on hat-8 with no container, no qemu and no apt.
- Configure flags: `--prefix=/opt/postgresql --without-icu --without-readline --without-zlib
  --without-openssl` plus every other optional library off. It is not `--with-systemd`. The make
  variable `rpathdir='$$ORIGIN/../lib'` gives RUNPATH `$ORIGIN/../lib`. The prefix contains
  "postgres", so pkglibdir = lib/ and sharedir = share/.
- Result: NEEDED only libc/libm/libpthread/libdl/librt, max GLIBC_2.27, own tzdata, no env vars.

  | arch | raw | zstd -19 | build time on hat-8 |
  |---|---|---|---|
  | amd64 | 16.2 MB | 4,853,858 bytes | 96-180 s |
  | arm64 | - | 4,380,281 bytes (cross) | 206 s |

- Not yet byte-reproducible: two amd64 runs in different work directories differed in
  `bin/postgres`, because pg_config's compiled-in CC, CFLAGS and configure arguments embed the
  work path. The fix is a fixed build path. The dry-run test (`--dry-run` prints every pin) would
  be committed with the script.

## 7. Rejected candidates (facts recorded before the user ruled repackagers out)

- **zonky** `io.zonky.test.postgres:embedded-postgres-binaries-linux-{amd64,arm64v8}:18.6.0`
  (Maven Central, jar sha256 amd64
  008ee189eaa7ca2b58bd01dd950291e638b998392cf982297acd4779ec898ac1, arm64
  5f54e016723afa6213925a8acb939bac52944b1d348f65c7a20f0cd2928c31be). Problems:
  - Only `initdb pg_ctl postgres`: no pg_dump and no psql.
  - Bundles EOL OpenSSL 1.1 and ICU 60.
  - The maven `.asc` is signed by key 6B02ED0ED53F9FD3, which is not retrievable from
    keyserver.ubuntu.com or keys.openpgp.org, so it is unverifiable.
- **theseus-rs** `postgresql-18.6.0-{x86_64,aarch64}-unknown-linux-gnu.tar.gz` (sha256
  bb3d09f8...ca61 and 5d2b5e8b...ee13). Problems:
  - Only a same-channel `.sha256`. GitHub attestation API returns 404.
  - Built on Debian 12 gcc 12 with `--without-icu --with-system-tzdata`.
  - Links host libssl3, libxml2, krb5, lz4, zstd and readline (not bundled).
  - The musl variants need a musl host.

## 8. Proposed internal/versions/versions.json entry (the lead edits the file)

```json
"postgres": {
  "version": "18.6",
  "how": "PGDG official debs (apt.postgresql.org jammy-pgdg 18.6-1.pgdg22.04+2: postgresql-18, postgresql-client-18, libpq5) plus their Ubuntu 22.04 shared-library closure (44 debs from jammy, jammy-updates, jammy-security), each verified by signed InRelease -> Packages.gz sha256 -> deb sha256, extracted without root (ar + tar) into <root>/usr/lib/postgresql/18/{bin,lib}, <root>/usr/share/postgresql/18, <root>/usr/lib/sys; see docs/research/2026-10-11-vm-lifecycle/pg-source.md for the 47 per-arch deb pins",
  "pgdgKeyFingerprint": "B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8",
  "ubuntuArchiveKeyFingerprint": "F6ECB3762474EDA9D21B7022871920D1991BC93C",
  "debs": {
    "linux/amd64": {"postgresql-18": "2aab00b13babd3d9abc257bc5f6b00efa1f7c27cda071413f358d2e0cdb84420", "postgresql-client-18": "ad4e491adceaf1453162c62691473ace78ed81ab20f50d11b344a793d13ed4bd", "libpq5": "8af0b458834e219f3aa7c904fa1237ea484950486e3165a3a90204878e060530"},
    "linux/arm64": {"postgresql-18": "8a379f0684b480b60e79657ac8178e8b00c835e7575e85ae746c091d336563d3", "postgresql-client-18": "21f1b418621fc9c90785b18ec2d7fe4fd8ff111cc607fc3b3625464fc6e7886b", "libpq5": "fc0d787c9da82bd0146e5e365989f3c92dbb3d73c3a32c62a2e4d56ab90933a6"}
  },
  "glibcFloor": "2.35",
  "icu": "70.1 (unicode collversion 153.112; unused: initdb --locale-provider=builtin --builtin-locale=C.UTF-8, datcollversion 1)",
  "invariant": "Relocatable ONLY with the deb layout kept (<root>/usr/lib/postgresql/18/bin; PostgreSQL resolves share and pkglib relative to that tail) and LD_LIBRARY_PATH=<root>/usr/lib/sys set on the postgres/initdb/pg_dump exec alone, never on the core (it leaks jammy libs into any child: grep under it loaded jammy libpcre2). Timezone files are the HOST's /usr/share/zoneinfo (compiled-in absolute path): check-host requires /usr/share/zoneinfo/UTC. postgres REFUSES -p 0 (GUC minimum 1): the kcp store is unix-socket only (listen_addresses=''), tenant stores lease. Runs as non-root only. Any Ubuntu jammy security update changes the closure pins."
}
```

Replace the existing `embedded-postgres` entry ("the host's own binaries ... on PATH") when
M2-POSTGRES lands. Its port and root invariants still hold.
