# socialweb-computer GitLab fixture project

`scripts/gitlab-fixture.sh up` creates this template as a project named `fixture-probe` on a fresh
GitLab CE fixture, and commits `deploy/gitlab-fixture/template/` to its `main` branch.

The project runs one job, `clone-probe`. The job runs on a runner tagged
`socialweb-computer-fixture` and proves that the runner cloned the project: it checks that
`.gitlab-ci.yml` and `README.md` are present and prints the commit SHA as `FIXTURE_CLONE_OK`.
