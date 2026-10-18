This is Vibe coded app for some better analysis and visualisation of nordpool data. 

## Battery planning API

`cmd/runserver` (Cloud Run image) and `cmd/serve` expose a JSON API on top of `pkg/planner`:

| Endpoint | What it does |
| --- | --- |
| `POST/GET /v1/battery/optimize` | Optimal charge/discharge plan that minimises electricity cost plus battery wear (`planner.OptimizeBattery`). |
| `POST/GET /v1/battery/solax` | The same optimal plan as SolaX DataHub 1000 recommendations (see below). |
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

### SolaX DataHub 1000 format

`/v1/battery/solax` takes the same request plus `timezone` (site IANA zone, default `UTC`) and `inverters`
(parallel inverters behind the DataHub, default 1) and `min_power_kw` (windows below it are skipped, default 0.5 kW,
since tiny top-ups only add noisy rules) and returns `format: "solax-datahub1000/v1"` with `windows`.
Consecutive slots with the same action and power are merged into one window; windows never cross local midnight,
so each maps to a single Smart Scene rule:

```json
{
  "date": "2025-12-08", "start_local": "03:00", "end_local": "04:00",
  "action": "charge", "power_kw": 8, "per_inverter_kw": 4,
  "energy_kwh": 8, "start_soc_percent": 10, "target_soc_percent": 90,
  "avg_price_per_mwh": 20,
  "smart_scene": "IF Date & Time 2025-12-08 03:00-04:00 THEN Inverter Charge as 8.0 kW till Battery SOC 90 %",
  "modbus": {"mode": "Enabled Power Control", "active_power_w": 8000, "per_inverter_power_w": 4000, "duration_s": 3600}
}
```

- `smart_scene` follows the DataHub Smart Scene IF/THEN wording ("Inverter Charge/Discharge as X kW till Battery SOC Y %").
  The SoC target makes the rule safe even if the real battery behaves differently from the model.
- `modbus` is for an EMS that drives inverters directly in remote-control "Enabled Power Control" mode: signed AC power
  in W (positive charges, negative discharges) for `duration_s`. Register addresses depend on the inverter generation and
  firmware, check them against SolaX's Modbus documentation before writing.
- Power is AC side (grid energy / slot length), rounded to 0.1 kW; prices are reported per MWh as DataHub expects.

```sh
curl 'localhost:8080/v1/battery/solax?area=LV&timezone=Europe/Riga&inverters=2&capacity_kwh=20&initial_soc_percent=30&degradation_cost_cents=2&max_cycles_per_day=1'
```

### Chrome extension

[`extension/solax-scenes`](extension/solax-scenes) loads this plan and fills Smart Scenes in the SolaX web UI by
replaying a macro you record once. See its README.

Tests use [quicktest](https://github.com/frankban/quicktest) assertions and `testing/quick` property checks:

```sh
go test ./...
```
