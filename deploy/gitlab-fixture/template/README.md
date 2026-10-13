# socialweb-computer GitLab fixture project

`scripts/gitlab-fixture.sh up` creates this project on a fresh GitLab CE fixture from
`deploy/gitlab-fixture/template/`. Its one job, `clone-probe`, runs on a runner tagged
`socialweb-computer-fixture` and proves that the runner cloned this repository from the fixture.
