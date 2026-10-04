# vetmimi-api

The Go API behind [VetMiMi](https://vetmimi-next.vercel.app), Daw Mi's art
therapy practice: appointment booking, online video sessions, content
management, media, and email. The public site and its `/admin` are in
[vetmimi-next](https://github.com/VetMiMi/vetmimi-next).

Work is tracked on the GitHub project
[VetMiMi Website](https://github.com/orgs/VetMiMi/projects/1); decisions are
in [`docs/adr/`](docs/adr/README.md); current state in
[`docs/project-status.md`](docs/project-status.md).

## Run it locally

Requirements: Go (current stable), PostgreSQL 17 and Redis from Homebrew, the
tools from `make tools`. No Docker needed on a laptop.

```sh
brew install go postgresql@17 redis sqlc golang-migrate
brew services start postgresql@17 && brew services start redis
createdb vetmimi && createdb vetmimi_test
make tools
cp .env.example .env            # fill in local values
make migrate
make dev                        # http://localhost:8080/healthz
make worker                     # in a second terminal: reminders, email, expiry
```

## Check it

```sh
make gate                       # lint + tests + build + generated-code drift, behind the RAM lock
make test-pkg PKG=./internal/booking
```

## Layout

See [`AGENTS.md`](AGENTS.md) for the layout, the rules and the delivery loop,
[`docs/architecture.md`](docs/architecture.md) for how the pieces fit and
[`docs/data-model.md`](docs/data-model.md) for the tables.
