import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { setAuthToken } from "../../../lib/auth";
import { login } from "../api";

export function useLogin() {
  const navigate = useNavigate();

  return useMutation({
    mutationFn: login,
    onSuccess: (data) => {
      setAuthToken(data.token);
      navigate("/", { replace: true });
    },
  });
}
