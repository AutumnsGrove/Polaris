// Drives a real browser through "press Stop while an answer is streaming" against a
// running `polaris run` that points at dev/fakeopenrouter. Issue #186 / #204: until
// this existed only a socket drop had been tested against the Nova loop, never the
// actual button.
//
// Setup (see dev/fakeopenrouter's doc comment for the queue API):
//
//	go run ./dev/fakeopenrouter -addr 127.0.0.1:18901 -chunk-delay 60ms &
//	# config.yaml: openrouter.base_url: "http://127.0.0.1:18901", server.port: 8898
//	polaris run --dev --config config.yaml &                       # Nova loop (the default)
//	POLARIS_NOVA_LOOP=0 polaris run --dev --config config.yaml &   # the bespoke loop, to compare
//	node dev/e2e/stop_midstream.cjs http://127.0.0.1:8898 http://127.0.0.1:18901
//
// -chunk-delay is what makes this possible: with 0 the whole answer arrives before
// the click lands. Exits nonzero on the first violated expectation and prints one
// JSON summary line on success, so a run on each loop can be diffed.
const { chromium } = require('playwright');

const BASE = process.argv[2] || 'http://127.0.0.1:8898';
const FAKE = process.argv[3] || 'http://127.0.0.1:18901';

const WORDS = 300;
const FULL = Array.from({ length: WORDS }, (_, i) => `Alpha-${String(i + 1).padStart(3, '0')}`).join(' ');
const FOLLOW_UP = 'Follow-up answer arrived intact.';

function check(cond, msg) {
	if (!cond) {
		console.error('FAIL:', msg);
		process.exitCode = 1;
		throw new Error(msg);
	}
}

async function post(path, body) {
	const r = await fetch(FAKE + path, { method: 'POST', body: body ? JSON.stringify(body) : undefined });
	check(r.ok, `${path} -> ${r.status}`);
}

// Highest "Alpha-NNN" marker rendered anywhere on the page, 0 if none yet.
async function progress(page) {
	const text = await page.locator('body').innerText();
	let max = 0;
	for (const m of text.matchAll(/Alpha-(\d{3})/g)) max = Math.max(max, Number(m[1]));
	return max;
}

(async () => {
	await post('/_control/reset');
	await post('/_control/queue', { responses: [{ content: FULL }, { content: FOLLOW_UP }] });

	const browser = await chromium.launch();
	try {
		const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
		await page.goto(BASE);

		const box = page.getByLabel('Ask Polaris').first();
		await box.fill('write a very long answer');
		await page.locator('button.send-btn[title="Send"]').click();

		// Wait until the answer is genuinely mid-stream, then press the real Stop button.
		await page.waitForFunction(() => /Alpha-02\d/.test(document.body.innerText), null, { timeout: 30000 });
		await page.locator('button.send-btn[title="Stop generating"]').click();

		// 1. Streaming actually stops: no growth after the click settles.
		await page.waitForTimeout(500);
		const atStop = await progress(page);
		await page.waitForTimeout(3000);
		const later = await progress(page);
		check(atStop >= 20, `expected to have stopped mid-answer, saw only ${atStop} words`);
		check(later === atStop, `answer kept streaming after Stop (${atStop} -> ${later})`);
		check(later < WORDS, 'the whole answer arrived; Stop had no effect');

		// 2. The composer recovered: the button is back to Send, not stuck on Stop.
		await page.locator('button.send-btn[title="Send"]').waitFor({ timeout: 5000 });
		check((await page.locator('button.send-btn[title="Stop generating"]').count()) === 0, 'still showing the Stop button');

		// 3. The partial answer was kept, as a normal finished message, across a reload.
		const url = page.url();
		await page.reload();
		await page.waitForFunction(() => /Alpha-0\d\d/.test(document.body.innerText), null, { timeout: 15000 });
		const afterReload = await progress(page);
		check(afterReload === atStop, `partial answer changed across reload (${atStop} -> ${afterReload})`);
		check((await page.locator('button.send-btn[title="Stop generating"]').count()) === 0, 'reload shows a turn still in flight');

		// 4. The thread is not wedged: a follow-up works and renders.
		await page.getByLabel('Ask Polaris').first().fill('thanks, now something short');
		await page.locator('button.send-btn[title="Send"]').click();
		await page.getByText(FOLLOW_UP).first().waitFor({ timeout: 30000 });

		const calls = await (await fetch(FAKE + '/_control/calls')).json();
		console.log(
			JSON.stringify({
				ok: true,
				url,
				wordsAtStop: atStop,
				wordsAfterReload: afterReload,
				modelCalls: Array.isArray(calls) ? calls.length : null
			})
		);
	} finally {
		await browser.close();
	}
})().catch((e) => {
	console.error(e.message);
	process.exit(1);
});
