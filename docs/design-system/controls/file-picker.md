# File picker

**Code.** `FilePicker` and `AttachmentChips` in `packages/ui`. Demo: [book](../book/index.html#file-picker).

**Purpose.** Choose the files a run reads ([projects](../pages/projects.md), Run on files). A message's files come through the [composer](composer.md); both show the chosen files with the same chips.

**Anatomy.** A dashed drop area on the sunken surface: the file icon, the prompt ("Drop the files to read, or") and a quiet **Pick files** button that opens the picker for several files at once. Under it, one chip per file: thumbnail or file icon, name, then size, upload percent or "failed", and a remove button; an uploading chip has a progress bar on its bottom edge.

**Behaviour.**
* Files dropped anywhere over the area are added (`DropZone`); while they are dragged over it, the dashed accent overlay shows the prompt and moves nothing (OR-23).
* The caller uploads each file as it is added and keeps the list; the picker only reports the files and the removals.
* A failed file is red with its error on hover; the caller's start action names it in its disabled reason.

**Keyboard.** Pick files is a button (`Tab`, `Enter`); each chip's remove is a button named "Remove <file>".

**Accessibility.** The chips are a list named "Files". The hidden file input is not in the tab order; the button opens it.

**Do.** Say in the prompt what the files are for. **Don't.** Float the picker (OR-14) or show a second list of the same files.

**Used in.** Projects (a pipeline page).
