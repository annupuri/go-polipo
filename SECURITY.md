Security Policy
===============

Supported versions
------------------

Security fixes go into the latest release only. Please check that a problem
still occurs on the latest release, or on `main`, before reporting it.

The original C Polipo (1.1.x) is no longer maintained, and none of its code
remains in this repository. Problems in it are out of scope here unless
go-polipo has them too.

Reporting a vulnerability
-------------------------

Please do not report security problems in a public issue, pull request or
discussion. Report them privately instead, through GitHub's private
vulnerability reporting:

1. Open the repository's **Security** tab.
2. Click **Report a vulnerability**.

Or go straight to
<https://github.com/annupuri/go-polipo/security/advisories/new>.

Please include, as far as you can:

- the go-polipo version (`polipo -v`) and the operating system;
- the configuration variables that matter (not your whole configuration if
  it contains private details);
- the steps or requests that trigger the problem, and what an attacker
  could do with it.

What to expect
--------------

go-polipo is maintained by one person in their spare time, so there is no
fixed response time. Reports are read and answered as soon as practical.
Once a problem is confirmed, the fix is made privately and released, and
the advisory is then published with credit to the reporter unless you ask
otherwise.

Scope notes
-----------

go-polipo caches only `http://` traffic. HTTPS goes through CONNECT tunnels
and is never decrypted or cached, so a report that go-polipo cannot see or
filter HTTPS content is expected behaviour, not a vulnerability.

By default go-polipo listens on `127.0.0.1` and accepts clients only from
the loopback addresses. Exposing it more widely is a configuration choice;
go-polipo warns at startup when it would act as an open proxy.
