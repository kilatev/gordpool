This is Vibe coded app for some better analysis and visualisation of nordpool data. 

## Battery planning API

`cmd/runserver` (Cloud Run image) and `cmd/serve` expose a JSON API on top of `pkg/planner`:

| Endpoint | What it does |
| --- | --- |
| `POST/GET /v1/battery/optimize` | Optimal charge/discharge plan that minimises electricity cost plus battery wear (`planner.OptimizeBattery`). |
| `POST/GET /v1/battery/schedule` | The original threshold strategy (`planner.BuildBatterySchedule`). |
| `GET /healthz` | Liveness probe. |

Prices are taken from Nordpool via the SQLite cache (`CACHE_DB`, defaults to `$TMPDIR/gordpool/prices.db`
for `runserver` and `data/prices.db` for `serve`), unless the request carries its own `prices` array.

The optimizer runs a dynamic program over the battery state of charge (1% steps) and keeps the battery healthy by:

- staying inside the SoC window (`min_soc_percent`/`max_soc_percent`, default 10–90%); a battery that starts outside
  it is only ever moved back towards it;
- respecting charge/discharge power (default 0.5C);
- charging a wear cost (`degradation_cost_cents` per discharged kWh) so it only cycles when the spread pays for it
  after round-trip losses (`round_trip_efficiency`, default 0.9);
- optionally capping equivalent full cycles (`max_cycles_per_day`);
- valuing energy left at the end (`stored_energy_value_cents`, default: median price) so it does not dump the battery
  at the end of the horizon.

```sh
curl -X POST localhost:8080/v1/battery/optimize -d '{
  "area": "LV",
  "battery": {
    "capacity_kwh": 10,
    "initial_soc_percent": 40,
    "degradation_cost_cents": 2,
    "max_cycles_per_day": 1
  }
}'

curl 'localhost:8080/v1/battery/optimize?area=LV&capacity_kwh=10&initial_soc_percent=40'
```

The response has a per-slot plan (`action`, `power_kw`, `grid_kwh`, SoC before/after), merged
`charge_intervals`/`discharge_intervals`, and a `summary` with cost, wear, cycles and `net_benefit_cents`
compared to leaving the battery idle.

Tests use [quicktest](https://github.com/frankban/quicktest) assertions and `testing/quick` property checks:

```sh
go test ./...
```
