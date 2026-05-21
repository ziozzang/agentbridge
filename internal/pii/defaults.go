package pii

func Defaults() []Pattern {
	return []Pattern{
		{Name: "kr_ssn", Regex: `\b\d{6}-?[1-4]\d{6}\b`, Mask: "[MASK_KR_SSN_{n}]"},
		{Name: "kr_mobile", Regex: `\b01[016-9][\s.-]?\d{3,4}[\s.-]?\d{4}\b`, Mask: "[MASK_KR_MOBILE_{n}]"},
		{Name: "kr_phone", Regex: `\b0(?:2|3[1-3]|4[1-4]|5[1-5]|6[1-4])[\s.-]?\d{3,4}[\s.-]?\d{4}\b`, Mask: "[MASK_KR_PHONE_{n}]"},
		{Name: "email", Regex: `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`, Mask: "[MASK_EMAIL_{n}]"},
		{Name: "credit_card", Regex: `\b(?:\d{4}[\s-]?){3}\d{3,4}\b`, Mask: "[MASK_CC_{n}]"},
		{Name: "ipv4", Regex: `\b(?:\d{1,3}\.){3}\d{1,3}\b`, Mask: "[MASK_IPV4_{n}]"},
		{Name: "jwt", Regex: `\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`, Mask: "[MASK_JWT_{n}]"},
		{Name: "openai_key", Regex: `\bsk-[A-Za-z0-9_-]{20,}\b`, Mask: "[MASK_API_KEY_{n}]"},
		{Name: "github_token", Regex: `\bgh[pousr]_[A-Za-z0-9_]{20,}\b`, Mask: "[MASK_GITHUB_TOKEN_{n}]"},
		{Name: "anthropic_key", Regex: `\bsk-ant-[A-Za-z0-9_-]{20,}\b`, Mask: "[MASK_API_KEY_{n}]"},
	}
}
