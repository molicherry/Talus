import { Loader2 } from "lucide-react";
import { type FormEvent, type RefObject, useEffect, useRef, useState } from "react";
import { Button } from "../../../components/ui/button";
import { Dialog } from "../../../components/ui/dialog";
import { Field, Input } from "../../../components/ui/field";
import { useTranslation } from "../../../i18n";
import { apiClient } from "../../../lib/api-client";
import { apiFieldErrors, translateApiError } from "../../../lib/api-error";
import { toast } from "../../../lib/toast";

export function ChangePasswordDialog({
  onClose,
  returnFocusRef,
}: {
  onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
}) {
  const { t } = useTranslation();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [error, setError] = useState("");
  const [newPasswordError, setNewPasswordError] = useState("");
  const [pending, setPending] = useState(false);
  const inFlight = useRef(false);
  const active = useRef(true);
  const currentInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (inFlight.current) return;
    inFlight.current = true;
    setPending(true);
    setError("");
    setNewPasswordError("");
    try {
      await apiClient.put("/api/v1/auth/password", {
        current_password: currentPassword,
        new_password: newPassword,
      });
      if (active.current) {
        toast.success(t("auth.passwordChanged"));
        onClose();
      }
    } catch (err) {
      if (active.current) {
        const fields = apiFieldErrors(err, t);
        if (fields.new_password) setNewPasswordError(fields.new_password);
        else setError(translateApiError(err, t, "common.error"));
      }
    } finally {
      inFlight.current = false;
      if (active.current) setPending(false);
    }
  };

  return (
    <Dialog
      open
      title={t("auth.changePassword")}
      onClose={onClose}
      dismissible={!pending}
      initialFocusRef={currentInput}
      returnFocusRef={returnFocusRef}
      className="max-w-sm"
    >
      <form onSubmit={submit} aria-busy={pending} className="space-y-4">
        <Field label={t("auth.currentPassword")} htmlFor="current-password">
          <Input
            ref={currentInput}
            id="current-password"
            type="password"
            autoComplete="current-password"
            required
            disabled={pending}
            value={currentPassword}
            onChange={(event) => setCurrentPassword(event.target.value)}
          />
        </Field>
        <Field
          label={t("auth.newPassword")}
          htmlFor="new-password"
          error={
            newPasswordError && (
              <span id="new-password-error" role="alert">
                {newPasswordError}
              </span>
            )
          }
        >
          <Input
            id="new-password"
            type="password"
            autoComplete="new-password"
            required
            minLength={8}
            aria-invalid={!!newPasswordError}
            aria-describedby={newPasswordError ? "new-password-error" : undefined}
            disabled={pending}
            value={newPassword}
            onChange={(event) => setNewPassword(event.target.value)}
          />
        </Field>
        {error && (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        )}
        <div className="flex gap-3">
          <Button type="submit" disabled={pending} className="flex-1">
            {pending && <Loader2 className="h-4 w-4 animate-spin" />}
            {t(pending ? "common.updating" : "common.save")}
          </Button>
          <Button
            type="button"
            variant="secondary"
            disabled={pending}
            onClick={onClose}
            className="flex-1"
          >
            {t("common.cancel")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
