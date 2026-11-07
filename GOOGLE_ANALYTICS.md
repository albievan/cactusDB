# Google Analytics for cactusdb: the plan

Not built yet. This is how it would work, what it would and wouldn't send, and what
you need to set up first. The [usage log](README.md#the-usage-log-what-is-used-in-cactusdb)
in `cactus.conn.db` stays as it is: it has the full detail, on your own server. Google
Analytics would add charts, trends over months and a view across several cactusdb servers,
with far less detail.

## How it would work

1. **Off unless you turn it on.** cactusdb gets a Google Analytics *Measurement ID*
   (`G-XXXXXXXXXX`) from a startup option or an environment variable:

   ```sh
   cactusdb --port 8080 --ga-id G-XXXXXXXXXX
   CACTUSDB_GA_ID=G-XXXXXXXXXX cactusdb --port 8080      # e.g. in deploy.sh's .env
   ```

   Without one, nothing changes: no Google script is loaded and nothing is sent. The ID
   isn't a secret: it's visible in every page that uses Google Analytics.

2. **The page loads Google's tag (`gtag.js`) only when an ID is set.** The server passes
   the ID to the page with `/api/info`. The script loads in the background; if a firewall
   or an ad blocker blocks Google, cactusdb works exactly the same and the events are
   simply not sent.

3. **The same events as the usage log, cleaned of anything internal.** The page already
   records every function in one place (`track()` in `web/index.html`). That place would
   also send each event to Google, translated to Google's naming rules (lowercase, `_`,
   at most 40 characters):

   | Usage log | Google Analytics event | Parameters |
   |---|---|---|
   | page opened | `page_view` | `page_title`: `cactusdb` |
   | `tab: logs` | `page_view` (a virtual page per tab: `/data`, `/definition`, `/logs`, `/compare`, …) | `page_location` |
   | `menu: compare` | `menu_click` | `item`: `compare` |
   | `connect` | `connect` | `engine`: `mariadb`, `result`: `ok` / `failed` |
   | `open table` | `open_table` | `engine` |
   | `run query` | `run_query` | `engine`, `result`, `duration_bucket`: `<1s`, `1-10s`, `>10s` |
   | `export`, `export all tables` | `export` | `format`: `xlsx` / `csv` / `json` / `xml`, `tables_bucket` |
   | `export DDL`, `backup` | `export_ddl`, `backup` | `engine` |
   | `compare databases` | `compare` | `databases`: 2, 3, …, `engines` |
   | `sort objects: rows` | `sort` | `by`: `rows` |
   | other buttons and check boxes | `ui_click` | `control`: the button's id, e.g. `cmpDrops` |

   Every event also gets `app_version` (e.g. `1.28.0`), so you can see who still runs an
   old version.

4. **Never sent to Google:** SQL, table, column or connection names, host names, database
   names, file paths, IP addresses, session ids, usernames, error messages (they can
   contain any of these), or anything typed. Google's terms forbid sending personal data,
   and these also say a lot about your systems. Only the names of functions and fixed
   values (engine, format, tab) leave the page.

5. **Privacy settings in the tag:** `allow_google_signals: false` and
   `allow_ad_personalization_signals: false`, so the data is never used for advertising.
   Google Analytics 4 doesn't store IP addresses. It does set a cookie (`_ga`) to tell
   browsers apart.

6. **An opt-out per browser:** ☰ → Settings would get *Send usage statistics to Google
   Analytics* (on by default when an ID is set, remembered in that browser), and the
   page would say once that usage statistics are collected. See the consent decision below.

7. **Events from the browser, not the server.** The browser sends straight to Google, so
   only the computers using cactusdb need internet access, not the server (the k3s node
   may well have none). Command-line exports (`cactusdb --dbtype …`) don't use a browser,
   so they wouldn't be counted. They could be, later, with Google's *Measurement Protocol*
   from the server; that needs an API secret and internet access from the server, so I'd
   leave it out at first.

## What you need to do

1. **Create a Google Analytics account and property**, if you have none:
   <https://analytics.google.com> → *Admin* → *Create* → *Property*. Name it e.g.
   `cactusdb`, set your time zone (e.g. South Africa) and currency, and choose
   *Business objectives* → *Get baseline reports*.
2. **Add a web data stream:** *Admin* → *Data streams* → *Add stream* → *Web*.
   - The *Website URL* is only a label; an internal address such as
     `http://10.66.1.136:8080` is fine.
   - Name it e.g. `cactusdb production`.
   - Under *Enhanced measurement*, **turn off** *Page changes based on browser history
     events*, *Form interactions*, *Site search* and *Video engagement*: cactusdb sends its
     own events, and the automatic ones would count wrong or record form fields.
     *Page views* and *Outbound clicks* can stay on.
3. **Copy the Measurement ID** (`G-…`, at the top right of the stream) and give it to me,
   or put it in `.env` as `CACTUSDB_GA_ID`. It isn't secret.
4. **Register the custom parameters**, or they're collected but don't appear in reports:
   *Admin* → *Custom definitions* → *Create custom dimension*, scope *Event*, one each for
   `engine`, `format`, `item`, `control`, `by`, `result`, `app_version`,
   `duration_bucket`, `tables_bucket`. Google allows 50 per property.
5. **Data retention:** *Admin* → *Data collection and modification* → *Data retention* →
   *14 months* (the default is 2 months for detailed explorations).
6. **Optional, filter your own testing:** *Admin* → *Data filters* → *Developer traffic*
   (cactusdb would send `debug_mode` while testing), and an *Internal traffic* rule if you
   want to leave out your own IP range.
7. **Check that it works:** after I build it, open cactusdb and watch *Reports* →
   *Realtime*, or *Admin* → *DebugView* with debug mode on. Events show within seconds
   there; the normal reports take up to 24–48 hours.

## Decisions for you before I build it

- **Consent:** is a notice plus an opt-out in Settings enough (an internal tool, your own
  staff), or must people opt *in* first? Under POPIA or GDPR, a company policy may require
  opting in, and then nothing is sent until someone says yes.
- **Which servers:** one Measurement ID for all cactusdb servers (with a `server`
  parameter such as `production` / `test` to tell them apart), or a data stream each?
- **Command-line exports:** leave them out (the plan), or count them from the server with
  the Measurement Protocol (needs an API secret and internet access from the server)?
- **Network:** do the computers that use cactusdb reach `www.googletagmanager.com` and
  `*.google-analytics.com`? If a proxy or firewall blocks them, nothing arrives, and the
  usage log is then the only record.
