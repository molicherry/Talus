Connect this profile to Talus by entering its **HTTPS** base URL and API key,
then selecting **Save**. Changes apply to later requests immediately.

The key is stored by dsh's credentials service, not supplied to the model or its
shell. Leave the key blank to keep it for the same URL; use the clear control and
save to remove it. Changing the URL requires a key for the new target. The host
adds `X-API-Key` inside TLS and does not follow redirects.

After upgrading an older installation, enter the key once here. Legacy settings
and API-key environment variables are ignored; saving clears the old key field.

Use the `talus` skill and `talus_request` for server management, SSH commands,
monitoring, files, and service relay. Existing Talus scopes still apply. The plugin
starts its credential broker automatically; no API-key shell variable is needed.
Binary uploads and downloads use the bundled helper instead of the host tool.

The default local credentials provider uses owner-only files, not an encrypted
OS keychain; stronger isolation from same-user processes requires OS controls.
