# Polaris in pictures

What Polaris actually looks like. It's built phone-first (you'll mostly use it one-handed, over Tailscale),
but the same app fills out a desktop window, so most screens are shown both ways. Everything here is real
output from a real instance, using a made-up person ("Jane Doe") and generic questions.

For what each feature does and how it's wired, see the [feature list](FEATURES.md); to run it yourself,
[SETUP.md](../SETUP.md).

---

## Ask, and get sources

Polaris decides for itself whether to search, read a page, or just answer, and shows its work: every
search and page read is a row you can open, and every claim carries a citation chip. Light and dark are
both first-class.

<p align="center"><img src="screenshots/chat-desktop-split.webp" alt="The same answer in dark and light themes, split diagonally" width="760"></p>
<p align="center"><img src="screenshots/chat-phone-split.webp" alt="The same answer on a phone, split diagonally between dark and light" width="300"></p>

## A start screen that isn't a blank box

Twinkling stars, a slow comet, and constellations that draw themselves in and fade out.

<table>
<tr>
<td width="68%"><img src="screenshots/start-desktop.webp" alt="Start screen on desktop"></td>
<td width="32%"><img src="screenshots/start-phone.webp" alt="Start screen on a phone"></td>
</tr>
</table>

## Results you can use, not paragraphs to decode

Recommendations come back as a cover-art carousel; weather comes back as a range card; a question that
needs real numbers runs in a locked-down sandbox and comes back as a chart and a file you can download.

<table>
<tr>
<td width="68%"><img src="screenshots/recommendations-desktop.webp" alt="Book recommendations as a carousel of covers"></td>
<td width="32%"><img src="screenshots/recommendations-phone.webp" alt="Book recommendations on a phone"></td>
</tr>
<tr>
<td width="68%"><img src="screenshots/code-exec-desktop.webp" alt="A themed chart and a CSV document card produced by the code sandbox"></td>
<td width="32%"><img src="screenshots/code-exec-phone.webp" alt="The chart and CSV card on a phone"></td>
</tr>
</table>

## Atlas: a search-results page, when you'd rather read the results yourself

Ranked results from your own SearXNG instance, with per-domain Block / Lower / Raise / Pin controls. End
a query with `?` for a fast sourced answer instead.

<table>
<tr>
<td width="68%"><img src="screenshots/atlas-desktop.webp" alt="Atlas search results"></td>
<td width="32%"><img src="screenshots/atlas-phone.webp" alt="Atlas search results on a phone"></td>
</tr>
</table>

## Memory you can see and edit

Durable facts and preferences it picked up (or that you told it), listed plainly. Edit or forget any of
them, or tell it in plain English.

<table>
<tr>
<td width="68%"><img src="screenshots/memory-desktop.webp" alt="The Memory panel listing four memories"></td>
<td width="32%"><img src="screenshots/memory-phone.webp" alt="The Memory panel as a bottom sheet on a phone"></td>
</tr>
</table>

## Fields: folders of conversations that share instructions

A Field groups related threads, gives them shared instructions and files, and can have its own defaults
for model and memory. Starred Fields are pinned in the sidebar.

<table>
<tr>
<td width="68%"><img src="screenshots/fields-desktop.webp" alt="The Fields list"></td>
<td width="32%"><img src="screenshots/fields-phone.webp" alt="The Fields list on a phone"></td>
</tr>
<tr>
<td width="68%"><img src="screenshots/field-detail-desktop.webp" alt="One Field, with its conversations"></td>
<td width="32%"><img src="screenshots/field-detail-phone.webp" alt="One Field on a phone"></td>
</tr>
</table>

## Pulsar: a saved question that runs on a schedule

Each run reports only what's new since last time. Below, a daily weather check that already knows the
reader runs metric and works night shifts.

<table>
<tr>
<td width="68%"><img src="screenshots/pulsar-report-desktop.webp" alt="A Pulsar report with a forecast table, a verdict, and a temperature range card"></td>
<td width="32%"><img src="screenshots/pulsar-report-phone.webp" alt="The same Pulsar report on a phone"></td>
</tr>
</table>
<p align="center"><img src="screenshots/pulsar-list-phone.webp" alt="The list of Pulsar routines" width="300"></p>

## The Daily: a morning newspaper, assembled fresh

About a dozen independent mini-generations (weather, a word, on this day, a quote, headlines, local,
sports, and any custom blocks you define), with a Top Story that gets deeper elaboration. Shown here at
desktop width, where it opens out to three columns.

<p align="center"><img src="screenshots/daily-desktop.webp" alt="The Daily on desktop in three columns" width="860"></p>

## Constellation: what it has learned about you, in cards

A background pass ("Weaver") reads your recent conversations and keeps short, evergreen cards, folding
repeats into one growing card instead of scattered notes.

<table>
<tr>
<td width="68%"><img src="screenshots/constellation-desktop.webp" alt="The Constellation library, grouped by category"></td>
<td width="32%"><img src="screenshots/constellation-phone.webp" alt="The Constellation library on a phone"></td>
</tr>
</table>

## Oracle mode: it reads your question first

Before answering, Oracle quietly decides how the question should be handled (extra care with sources on a
health question, a table for a comparison, fresher sources for breaking news). The little constellation
below is that reading step: one star per check, joined by a line once it's decided. Anything it changes
is shown on the reply and can be undone with a tap. It's off by default.

<p align="center"><img src="screenshots/oracle-desktop.gif" alt="The Oracle reading animation: stars light up one by one, then join into a line" width="480"></p>

<table>
<tr>
<td width="68%"><img src="screenshots/oracle-settings-desktop.webp" alt="The Oracle mode section of Settings"></td>
<td width="32%"><img src="screenshots/oracle-settings-phone.webp" alt="The Oracle mode settings on a phone"></td>
</tr>
</table>

See [oracle.md](oracle.md) for every check and what it does.

## Transponder: hands-free, full-screen

Push to talk, hear the answer spoken back, keep going without touching the keyboard, with sources still
visible on screen.

<p align="center"><img src="screenshots/transponder-phone.webp" alt="The Transponder call screen" width="300"></p>

## Settings, and a glossary for the made-up names

Pulsar, Constellation, Fields, Weaver... the **?** button in the sidebar translates each one into plain
English ("Fields = Projects").

<table>
<tr>
<td width="68%"><img src="screenshots/settings-desktop.webp" alt="The Settings panel"></td>
<td width="32%"><img src="screenshots/settings-phone.webp" alt="The Settings panel on a phone"></td>
</tr>
<tr>
<td width="68%"><img src="screenshots/help-desktop.webp" alt="The What's what glossary"></td>
<td width="32%"><img src="screenshots/help-phone.webp" alt="The glossary on a phone"></td>
</tr>
</table>
