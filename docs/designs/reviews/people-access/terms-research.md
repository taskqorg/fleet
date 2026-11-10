# Subscription terms for Fleet people access

Web review, sources accessed 2026-11-10. Not legal advice. **Fact** = provider text; **Report** = press or forums; **Speculation** = guess.

## 1. Anthropic and OpenAI

**Anthropic (Pro, Max, Ultra fall under the Consumer Terms)**

- (a) Own machines, headless agents. **Fact:** automated access is banned "except … via an Anthropic API Key or where we otherwise explicitly permit it" [1]; Claude Code is permitted, and limits "assume ordinary, individual usage of Claude Code and the Agent SDK" [2]. No machine-count limit found. Weekly limits (Aug 2025) target running Claude Code "continuously in the background, 24/7" [3][4]: allowed, but limits drain faster.
- (b) Other people. **Fact:** "You may not share your Account login information … You also may not make your Account available to anyone else" [1]. No routing requests "through Free, Pro, Max, or Ultra plan credentials on behalf of their users"; "each end user must authenticate with their own … credentials" [2]. No clause found against the subscriber personally doing work for someone else's project.
- (c) **Fact:** OAuth is for "ordinary use of Claude Code and other native Anthropic applications"; no one may "collect, store, or intermediate Claude.ai credentials or session tokens"; enforcement "without prior notice" [2]. Sharing and reselling are named violations [3]. **Report:** third-party harness blocks and bans Jan–Feb 2026 [5]; since Apr 2026 such harnesses bill as extra usage only [6].
- (d) **Fact:** Team: 2+ seats, Standard $20–25, Premium $100–125, Claude Code included. Enterprise: $20/seat + API-rate usage, 20-seat minimum [7][8]. Team, Enterprise, API are under Commercial Terms; API keys are the route for programmatic products [2].

**OpenAI (Plus/Pro fall under the Terms of Use, effective 2026-01-01)**

- (a) **Fact:** Codex docs show copying `~/.codex/auth.json` to headless machines, but "API keys are still the recommended default for automation" [9].
- (b) **Fact:** "You may not share your account credentials or make your account available to anyone else" [10].
- (c) **Fact:** no "circumvent any rate limits or restrictions" [10]. **Report:** the head of Codex endorsed subscriptions in third-party harnesses [5]; looser than Anthropic and Google.
- (d) **Fact:** Business: 2+ seats, Standard $20–25, Premium $100–125, Codex included, credits extend usage; Codex-only seats are usage-billed [11][12]. Enterprise can issue Codex access tokens for automation [9].

## 2. Google Antigravity / Gemini

**Fact:** "Using third party software, tools, or services to access the Service (e.g. using OpenClaw with Antigravity OAuth) is a breach", grounds for suspending Antigravity and Gemini CLI [13]. Google banned users of "3rd party tools or proxies", bulk-unbanned, and made a second violation permanent [14]. **Report:** AI Ultra subscribers locked out in Feb–Mar 2026 [15]. Strictest of the three.

## 3. CLIProxyAPI and Theo's gist

**Fact:** CLIProxyAPI wraps CLI logins as one compatible API with multi-account routing [16]. The gist signs 7 Claude and 3 Codex accounts into one home box that 12+ machines reach over Tailscale [17].

**Why one hub (author's claims):** no per-device logins; "constantly swapping the IP address … is practically begging for a ban"; avoid VPS/VPN because "Anthropic is aggressive about sign-ins from datacenter IPs" [17].

**Stated risks:** "This might get you banned. This might be against the terms of service." Rules: don't serve other people's traffic; Claude only via Claude Code. His routing picks the account whose quota resets soonest, i.e. cross-account balancing [17].

**Evidence of bans:**
- **Fact:** Anthropic and Google both banned accounts that used subscription OAuth outside the vendor's own client [5][14].
- **Anecdote:** 30+ accounts, one ban (VPN plus someone else's card) [17]. Reddit reports bans after changing computers; causes unverified [18].
- **Speculation:** IP/device hopping alone triggers bans; no provider says so. No provider text bans one person using their own login on several own machines.

## 4. Guidance for Fleet

**Looks safe:**
- Own subscription, own machines, vendor's unmodified CLI, work the person directs. Several machines and parallel agents are fine; weekly limits apply.
- Owner's agents doing tasks for Alina's or Simon's project: still the owner's own use, provided they get no login, token or way to drive his session.

**Risky:**
- Pooling several people's accounts in one hub, or two people on one subscription (no-sharing clauses [1][10]).
- Fleet storing or relaying other people's OAuth tokens [2].
- Round-robin across accounts to beat limits [3][10].
- Third-party proxies holding Claude or Antigravity OAuth tokens: gray for Claude, banned for Antigravity [2][13].
- Datacenter/VPN sign-ins (reported, not a rule).

**Use instead:**
- Claude Team or ChatGPT Business seats for a shared org (admin, spend caps, Commercial Terms).
- Per-person or per-project API keys with budgets for automated or shared runs [2][9].
- Fleet routes work to the account owner's machine, never moves credentials.

## Sources

1. https://www.anthropic.com/legal/consumer-terms
2. https://code.claude.com/docs/en/legal-and-compliance
3. https://x.com/AnthropicAI/status/1949898502688903593
4. https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users
5. https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546
6. https://dev.to/stevengonsalvez/anthropic-just-killed-third-party-harnesses-and-im-properly-gutted-dfk
7. https://support.claude.com/en/articles/9266767-what-is-the-team-plan
8. https://claude.com/pricing
9. https://developers.openai.com/codex/auth
10. https://openai.com/policies/row-terms-of-use/
11. https://help.openai.com/en/articles/8792828-chatgpt-business-overview
12. https://help.openai.com/en/articles/8792536-managing-billing-and-seats-in-chatgpt-business
13. https://antigravity.google/terms
14. https://github.com/google-gemini/gemini-cli/discussions/20632
15. https://www.theregister.com/software/2026/02/23/google-antigravity-falls-to-earth-under-compute-burden/4676154
16. https://github.com/router-for-me/CLIProxyAPI
17. https://gist.github.com/t3dotgg/59a521d2ee8d70aaf90e280c175072af
18. https://www.reddit.com/r/ClaudeCode/comments/1ruyhmg/claude_banned_my_paid_account_right_after_i
