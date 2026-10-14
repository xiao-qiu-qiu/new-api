import { useCallback, useLayoutEffect, useRef } from "react";

/** Stable transport callbacks for the vendored React 19.1 type baseline. */
export function useLocalAgentEvent<Args extends unknown[], Result>(callback: (...args: Args) => Result): (...args: Args) => Result {
    const callbackRef = useRef(callback);
    useLayoutEffect(() => { callbackRef.current = callback; });
    return useCallback((...args: Args) => callbackRef.current(...args), []);
}
