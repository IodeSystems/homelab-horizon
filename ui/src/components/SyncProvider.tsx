import { createContext, useContext, type ReactNode } from "react";
import SyncModal, { useSyncModal } from "./SyncModal";

interface SyncContextValue {
  startSync: () => void;
  isSyncing: boolean;
}

const SyncContext = createContext<SyncContextValue>({
  startSync: () => {},
  isSyncing: false,
});

export function useSyncContext() {
  return useContext(SyncContext);
}

export default function SyncProvider({ children }: { children: ReactNode }) {
  const { open, log, done, success, confirming, startSync, confirmSync, cancelSync, dismiss } =
    useSyncModal();

  return (
    <SyncContext.Provider value={{ startSync, isSyncing: open && !done && !confirming }}>
      {children}
      <SyncModal
        open={open}
        log={log}
        done={done}
        success={success}
        confirming={confirming}
        onConfirm={confirmSync}
        onCancel={cancelSync}
        onDismiss={dismiss}
      />
    </SyncContext.Provider>
  );
}
