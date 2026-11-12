# Google Analytics in cactusdb

cactusdb can send usage statistics to Google Analytics 4: which functions are used, how
often, with which database types and export formats, and by which cactusdb version. The
[usage log](README.md#the-usage-log-what-is-used-in-cactusdb) in `cactus.conn.db` keeps
the full detail on your own server either way; Google Analytics adds charts, trends over
months and a view over time without running SQL.

## How it works

- **One Measurement ID per cactusdb server.** Each server gets its own (production, test,
  …), set in ☰ → ⚙ Settings or with `CACTUSDB_GA_ID` (which wins, e.g. in `deploy.sh`'s
  `.env`). Without one, nothing is loaded and nothing is sent.
- **Opt-in, per person.** When a server has an ID, everyone is asked once, in a bar at the
  bottom of the page: *May cactusdb send anonymous usage statistics to Google Analytics?*
  - Until they click **Yes, send**, Google's script isn't even loaded.
  - **No, thanks** is remembered too, and they're not asked again.
  - Either answer can be changed any time in ☰ → ⚙ Settings → *Send usage statistics from
    this browser*.
  - The answer is kept in that browser (for that cactusdb address), so each person
    decides for themselves.
- **Blocked Google never matters.** The browser loads Google's script in the background.
  When a proxy, firewall or ad blocker stops it, or Google doesn't answer within 20
  seconds, cactusdb stops trying and nothing else changes: no error, no delay, no waiting.
  The browser itself may note the blocked script in its developer console. Statistics
  sent while Google is reachable are queued and sent in the background, never holding up
  a click.
- **Command-line runs** (`cactusdb --dbtype …`: exports, DDL, backups, `--showtable`) are
  sent by the server with Google's *Measurement Protocol*, but only:
  - when *Send command-line runs on this server too* is switched on in ☰ → ⚙ Settings;
  - and when an **API secret** is set there, or in `CACTUSDB_GA_API_SECRET`.

  The run is finished first; cactusdb then waits at most 2 seconds for Google and ignores
  any error, so the exit code and output never change. A proxy in `HTTPS_PROXY` is used.
  Command-line runs are always recorded in the usage log as well (`source` = `cli`).
- **No advertising use:** Google signals and ad personalisation are off, and the consent
  settings deny every advertising use. Google Analytics 4 doesn't store IP addresses. It
  does set a cookie (`_ga`), only after the person said yes, to tell browsers apart.
- The API secret is stored encrypted in `cactus.conn.db`, like saved passwords, and never
  sent to the page.

## What is sent

| Event | When | Parameters |
|---|---|---|
| `page_view` | the page opens, and each tab (a virtual page: `/data`, `/def`, `/logs`, `/compare`, `/migrate`, `/files`, `/queries`, `/dashboard`, `/relations`) | `page_location`, `page_title` |
| `connect`, `disconnect`, `test_connection` | connecting | `engine`, `result` (`ok`/`failed`), `duration_bucket` |
| `open_table`, `show_definition`, `run_query`, `table_stats`, `relationships` | browsing | `engine`, `result`, `duration_bucket` |
| `export`, `export_all`, `export_ddl`, `backup` | exporting | `engine`, `format`, `result`, `duration_bucket` |
| `compare` | ☰ → Compare databases | `engine`, `databases` (how many), `result` |
| `migrate` | ☰ → Migrate database | `engine`, `target` (the new database's type, `_script` for a script only), `result` |
| `save_connection`, `delete_connection`, `save_query`, `delete_saved_query`, `zip_files`, `preview_file`, `delete_files`, `save_settings`, `usage_housekeeping` | the other functions | `result`, `duration_bucket` |
| `menu_click` | a ☰ menu item | `item` (e.g. `compare`) |
| `sort`, `object_tab` | sorting the object list, its tabs | `by`, `type` |
| `ui_click`, `ui_toggle`, `ui_choose` | buttons, check boxes and drop-down lists with a fixed id | `control` (the id, e.g. `cmpDrops`); never the chosen value |
| `cli_export`, `cli_export_ddl`, `cli_backup`, `cli_show_tables` | command-line runs | `engine`, `format`, `tables_bucket`, `result`, `duration_bucket` |

Every event also has `app_version` (e.g. `1.51.0`).

**Never sent:** SQL, table, column, connection, host or database names, file paths, IP
addresses, session ids, usernames, error messages, or anything typed. Google's terms
forbid sending personal data, and these would also say a lot about your systems.

## Set it up

For **each** cactusdb server:

1. **Create a property** (once per server, or once in all if you prefer one property with
   a data stream per server): <https://analytics.google.com> → *Admin* → *Create* →
   *Property*. Name it e.g. `cactusdb production`, set your time zone and currency, and
   choose *Business objectives* → *Get baseline reports*.
2. **Add a web data stream:** *Admin* → *Data streams* → *Add stream* → *Web*.
   - The *Website URL* is only a label: the server's address, e.g.
     `http://10.66.1.136:8080`, is fine.
   - Under *Enhanced measurement*, **turn off** *Page changes based on browser history
     events*, *Form interactions*, *Site search* and *Video engagement*: cactusdb sends its
     own events, and those would count wrongly or look at form fields. *Page views*,
     *Scrolls* and *Outbound clicks* may stay on.
3. **Copy the Measurement ID** (`G-…`, top right of the stream) into ☰ → ⚙ Settings →
   *Measurement ID of this cactusdb server* → **Save**. Or set `CACTUSDB_GA_ID=G-…` where
   cactusdb runs (with `deploy.sh`: in `.env`) and restart it.
4. **For command-line runs** (optional): in the stream, *Measurement Protocol API secrets*
   → *Create*, copy the value into ☰ → ⚙ Settings → *API secret* (or
   `CACTUSDB_GA_API_SECRET`), tick *Send command-line runs on this server too* and **Save**.
   The server needs to reach `www.google-analytics.com` for this, possibly through
   `HTTPS_PROXY`.
5. **Register the parameters**, or they're collected but don't appear in reports:
   *Admin* → *Custom definitions* → *Create custom dimension*, scope *Event*, one each for
   `engine`, `format`, `item`, `control`, `by`, `type`, `result`, `app_version`,
   `duration_bucket`, `tables_bucket`. (`databases` can be a *custom metric*.)
6. **Data retention:** *Admin* → *Data collection and modification* → *Data retention* →
   *14 months* (the default is 2).
7. **Check it:** open cactusdb, answer **Yes, send**, click around, and watch *Reports* →
   *Realtime* (within a minute). The other reports fill in within 24–48 hours.

To stop: empty the Measurement ID in ☰ → ⚙ Settings (and unset `CACTUSDB_GA_ID`). Nothing
is loaded or sent from then on; the answers people gave stay in their browsers in case it
is switched on again.

## Network

- Browsers that may send statistics must reach `www.googletagmanager.com` and
  `*.google-analytics.com` (directly or through their proxy). Where they can't, nothing
  arrives in Google Analytics, and cactusdb works as usual.
- The cactusdb server needs to reach `www.google-analytics.com` only for command-line runs.
