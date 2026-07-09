import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { BrowserRouter } from "react-router-dom";
import { Toaster } from "sonner";
import i18n from "../i18n";
import { queryClient } from "./query-client";

interface ProvidersProps {
  children: ReactNode;
}

export function Providers({ children }: ProvidersProps) {
  return (
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          {children}
          <Toaster
            position="top-right"
            toastOptions={{
              style: {
                background: "white",
                color: "var(--color-gray-900)",
                border: "1px solid var(--color-gray-200)",
              },
            }}
          />
        </BrowserRouter>
      </QueryClientProvider>
    </I18nextProvider>
  );
}
