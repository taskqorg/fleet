package main

import "testing"

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
