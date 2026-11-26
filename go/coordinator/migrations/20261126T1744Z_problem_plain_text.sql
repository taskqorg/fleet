-- Problem words without links (OR-38): an open Bugsink problem's issue link moves from its text into data.url; an
-- open delivery problem's links already sit in data.links and its text is rewritten by the next delivery check.
UPDATE problem SET
 data = data || jsonb_build_object('url', substring(text from '(https?://\S+/issues/issue/[^/\s]+/)')),
 text = regexp_replace(text, ':\s*https?://\S+.*$', '.')
WHERE state = 'open' AND cause LIKE 'problem:bugsink:%' AND text ~ 'https?://';
