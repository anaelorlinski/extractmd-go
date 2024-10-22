module github.com/anaelorlinski/extractmd-go

go 1.26.0

require (
	github.com/ledongthuc/pdf v0.0.0-20261020125853-5e46805f718e
	github.com/richardlehane/mscfb v1.0.9
)

require github.com/richardlehane/msoleps v1.0.3 // indirect

// Patch stack on top of upstream: https://github.com/anaelorlinski/ledongthuc-pdf (branch custom-patchstack)
replace github.com/ledongthuc/pdf => github.com/anaelorlinski/ledongthuc-pdf v0.0.0-20261022130331-bff09c2136ea
