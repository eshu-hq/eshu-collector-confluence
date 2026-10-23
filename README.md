# eshu-collector-confluence

Pre-release. Holds no producer authority until its reviewed cutover
(eshu-hq/eshu#8855).

A read-only Confluence collector for [Eshu](https://github.com/eshu-hq/eshu),
built as a component extension on the public collector SDK. It reads one
Confluence space or one root page tree per claim and emits source-neutral
`documentation_*` facts.

This repository starts from the Eshu collector template. The extraction work is
tracked in eshu-hq/eshu#8855 under epic eshu-hq/eshu#6707.
