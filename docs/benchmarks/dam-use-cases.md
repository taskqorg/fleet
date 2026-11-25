# DAM Use Cases

Use cases of the DAM project (revilio.com, YouTrack LBG2-124) indexed for [Fleet Lab](../designs/fleet-lab.md) in [`config/lab/use-cases/dam.json`](../../config/lab/use-cases/dam.json).

<a id="document-recognition"></a>

## Model Document Recognition

Source: this section; Lab registry entry `dam:document-recognition`; Fleet task T-DAM-1.

- **Task shape:** One model's files (ID cards, passports, selfies with an ID, model releases, 18 U.S.C. 2257 performer records and unrelated files) are classified, and from each document the alias, legal name, date of birth, site or producing company and the presence of the model's signature are extracted. Across the files: re-scans of the same document, differing spellings of the legal name and unreadable files are flagged.
- **Constraint:** The data is personal. Recognition runs only on Fleet's own model accounts (subscriptions and our API keys), never a third-party OCR service.
- **Expected outcome:** A JSON result per model, `{documents: [{file, type, alias, legal_name, dob, site, signature, readable}], model: {legal_name, aliases, name_variants, duplicates, unreadable}}`, that the DAM agent can store and review.
- **Checks:** One script check per document field and per model-level flag against a labelled answer (`references/score.py` in the `dam-docs-*` examples); the `metric` score is the share of matching fields. Synthetic examples use documents of an invented state marked SPECIMEN; real DAM documents enter Lab only as a small sample processed through our own accounts.
- **Pipelines:** `doc-recognize-agent` (a subscription agent reads the images itself), `doc-recognize-anthropic`, `doc-recognize-openai` and `doc-recognize-gemini` (one API call with all images), and `doc-recognize-two-step` (a cheap classification call, then a strong extraction call on identity documents only). All share the prompt and result normalization in [`scripts/lab/doc_recognize.py`](../../scripts/lab/doc_recognize.py).
