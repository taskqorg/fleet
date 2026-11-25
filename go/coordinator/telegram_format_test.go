package main

import (
	"testing"
	"time"
)

func TestTelegramHTML(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"**Needs you**: 2 questions", "<b>Needs you</b>: 2 questions"},
		{"connection `openai` and <x> & y", "connection <code>openai</code> and &lt;x&gt; &amp; y"},
		{"## Итог\n- one *two*\n```\na < b **c**\n```", "<b>Итог</b>\n• one <i>two</i>\n<pre>\na &lt; b **c**\n</pre>"},
		{"see [FLT-31](https://youtrack.ufobe.com/issue/FLT-31)", `see <a href="https://youtrack.ufobe.com/issue/FLT-31">FLT-31</a>`},
		{"snake_case_name stays", "snake_case_name stays"},
		{"```\ncut off", "<pre>\ncut off</pre>"},
		{"**команда `/new`**", "<b>команда <code>/new</code></b>"},
		{"Fleet **`1.261125.63`** is running", "Fleet <b><code>1.261125.63</code></b> is running"},
		{"`a*b*c` and *x*", "<code>a*b*c</code> and <i>x</i>"},
	} {
		if got := tgHTML(c.in); got != c.want {
			t.Errorf("tgHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTelegramRich(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 10, 0, 0, time.UTC)
	for _, c := range []struct{ in, want string }{
		{"**v `1.2`** ok", "**v `1.2`** ok"},
		{"a <b> & `<c>`", "a &lt;b> &amp; `<c>`"},
		{`already \$5`, `already \$5`},
		{"fee $2,52 or **$0,05 via BEP20**, `$x`", `fee \$2,52 or **\$0,05 via BEP20**, ` + "`$x`"},
		{"line one\nline two\n\n- item\n- item", "line one  \nline two\n\n- item\n- item"},
		{"#89 stays open\n## Real", `\#89 stays open` + "\n## Real"},
		{"see docs/designs/fleet-telegram.md, and fleet.md.", "see `docs/designs/fleet-telegram.md`, and `fleet.md`."},
		{"fleet.ufobe.com v1.2.3 /help go/coordinator e.g.", "fleet.ufobe.com v1.2.3 /help go/coordinator e.g."},
		{"[fleet.md](https://x/fleet.md)", "[fleet.md](https://x/fleet.md)"},
		{"at 23:50 UTC", "at ![23:50 UTC](tg://time?unix=1795564200&format=t)"},
		{"at 2026-11-25T13:47:00Z", "at ![2026-11-25T13:47:00Z](tg://time?unix=1795614420&format=dt)"},
		{"```\nx <y> fleet.md\n13:00 UTC\n```", "```\nx <y> fleet.md\n13:00 UTC\n```"},
		{"| a | b |\n|---|---|\n| 1 | 2 |", "| a | b |\n|---|---|\n| 1 | 2 |"},
	} {
		if got := tgRich(c.in, now); got != c.want {
			t.Errorf("tgRich(%q) =\n %q, want\n %q", c.in, got, c.want)
		}
	}
}

func TestTelegramRichTimesAndTags(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	got := tgRich("from 2026-11-25 13:47 UTC to 14:00 UTC\n\n<details><summary>log</summary>\n\nx <y>\n</details>", now)
	want := "from ![2026-11-25 13:47 UTC](tg://time?unix=1795614420&format=dt) to ![14:00 UTC](tg://time?unix=1795615200&format=t)\n\n" +
		"<details><summary>log</summary>\n\nx &lt;y>\n</details>"
	if got != want {
		t.Errorf("got\n %q, want\n %q", got, want)
	}
}
