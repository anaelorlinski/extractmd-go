module github.com/anaelorlinski/extractmd-go

go 1.26.0

require (
	github.com/ledongthuc/pdf v0.0.0-20261020125853-5e46805f718e
	github.com/richardlehane/mscfb v1.0.9
)

require github.com/richardlehane/msoleps v1.0.3 // indirect

// Patch stack on top of upstream: https://github.com/anaelorlinski/ledongthuc-pdf (branch patchstack-1)
replace github.com/ledongthuc/pdf => github.com/anaelorlinski/ledongthuc-pdf v0.0.0-20261022134345-34ac569769fd
