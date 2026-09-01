# bird-list

Groundwork for a Go web app to track bird sightings.

## Screenshot

![Main page](docs/screenshot.png)

## Features currently scaffolded

- SQLite-backed persistence for sightings
- Home page at `/` with:
  - Lifetime sighting count
  - Filter controls for year and location (`uk`, `global`, `western_palearctic`)
  - Notable recent additions (most recently added first sightings per species)
- Species search via `/species?name=...` showing all sightings for a species
- Interactive cumulative species-count graph at `/stats`
- Import BTO BirdTrack `.xlsx` exports from the `Records#1` worksheet

## Run locally

```bash
go run ./cmd/birdlist
```

Then open `http://localhost:8080`.

Use a different port if needed:

```bash
go run ./cmd/birdlist -port 9090
```

By default, data is stored in `data/birds.db`.
Override with `BIRD_LIST_DB` if needed.

## Run on arm64 with Docker

The Docker configuration targets Linux arm64 systems, including Raspberry Pi
devices. It persists the SQLite database in `./data` and restarts the
application after Docker starts at boot.

```bash
docker compose up --build -d
```

Open `http://localhost:8080`. To select another host port, set
`BIRD_LIST_PORT` before starting the container:

```bash
BIRD_LIST_PORT=8081 docker compose up --build -d
```

## Install on Linux arm64

Run the installer directly from a checkout:

```bash
./bird-list-install.sh
```

It installs Git and Docker when needed, clones or updates the application in
`~/bird-list`, finds a free port starting at `8080`, starts the arm64 Docker
container, and configures it to restart after system startup. When updating a
running installation, it stops the container first and restarts it on its
existing port. The installer may ask for your `sudo` password when installing
system packages. If Docker was just installed, sign out and back in before
using Docker commands manually.

## Uninstall

```bash
~/bird-list/bird-list-uninstall.sh
```

The uninstaller stops and removes the container and application files. It asks
whether to remove sightings; if retained, it saves them in
`~/.local/share/bird-list` and restores them during a later installation.
Docker and Git are left installed for other applications.
