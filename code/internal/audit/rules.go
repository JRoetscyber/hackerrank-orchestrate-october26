package audit

// configJSON is the audit rule configuration: which data category each resource-id prefix maps to,
// structural hints for ambiguous prefixes, and how worker activity is attributed to cases.
// Edit this when a new kind of resource or system appears; no other code needs to change.
const configJSON = `{
  "_comment": "Maps a resource-id prefix to a deletion_rules.csv data_category. Edit this file (not code) when a new kind of resource appears.",
  "prefixes": {
    "account": {
      "categories": [
        "USER_PROFILE"
      ]
    },
    "search": {
      "categories": [
        "SEARCH_INDEX_ENTRY"
      ]
    },
    "cache": {
      "categories": [
        "CACHE_ENTRY"
      ]
    },
    "support": {
      "categories": [
        "SUPPORT_MESSAGE",
        "SUPPORT_ATTACHMENT"
      ]
    },
    "analytics": {
      "categories": [
        "ANALYTICS_PROFILE",
        "MARKETING_PROFILE"
      ]
    },
    "telemetry": {
      "categories": [
        "PRODUCT_TELEMETRY"
      ]
    },
    "export": {
      "categories": [
        "USER_EXPORT"
      ]
    },
    "marketing": {
      "categories": [
        "MARKETING_PROFILE"
      ]
    },
    "email_delivery": {
      "categories": [
        "EMAIL_DELIVERY_LOG"
      ]
    },
    "audit_record": {
      "categories": [
        "SECURITY_AUDIT_LOG"
      ]
    },
    "legal_hold": {
      "categories": [
        "LEGAL_HOLD_RECORD"
      ]
    },
    "backup": {
      "categories": [
        "BACKUP_ARCHIVE"
      ]
    },
    "tombstone": {
      "categories": [
        "ACCOUNT_TOMBSTONE"
      ]
    },
    "data_flow": {
      "categories": [
        "DATA_FLOW"
      ]
    }
  },
  "activity_source_system": "deletion_worker",
  "shape_hints": {
    "file_name": "SUPPORT_ATTACHMENT",
    "storage_class": "SUPPORT_ATTACHMENT",
    "uploaded_by": "SUPPORT_ATTACHMENT",
    "file": "SUPPORT_ATTACHMENT",
    "email_opt_in": "MARKETING_PROFILE"
  },
  "event_action": {
    "DELETE_RECORD": "DELETE",
    "REDACT_RECORD": "REDACT",
    "ANONYMIZE_RECORD": "ANONYMIZE",
    "RETAIN_RECORD": "RETAIN"
  },
  "attribution": {
    "account": {
      "event_type": "DELETE_RECORD",
      "window_min": [
        -120,
        15
      ],
      "result_consistent": true,
      "center": "zero"
    },
    "analytics": {
      "event_type": "ANONYMIZE_RECORD",
      "window_min": [
        -60,
        15
      ],
      "result_consistent": false,
      "center": "learned"
    }
  },
  "result_for_reported_status": {
    "COMPLETE": "SUCCESS",
    "PARTIALLY_COMPLETE": "PARTIAL",
    "NOT_STARTED": "FAILED"
  },
  "_attribution_comment": "Resources with no identifier (already-deleted accounts, anonymized profiles) are tied to a case by worker activity near the case's reported_at, one resource per case, matched globally."
}`
