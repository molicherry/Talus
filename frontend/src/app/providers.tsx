import { Fragment, type ReactNode } from "react";
import { BrowserRouter } from "react-router-dom";
import { Toaster } from "../lib/toast";

interface ProvidersProps {
  children: ReactNode;
}

export function Providers({ children }: ProvidersProps) {
  return (
    <Fragment>
        <BrowserRouter>
          {children}
          <Toaster />
        </BrowserRouter>
      </Fragment>
  );
}
