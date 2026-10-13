import { create } from "zustand";
import { getSiteContext } from "@/integration/site-context";
export type ThemeName = "light" | "dark";
type ThemeStore = { theme: ThemeName; setTheme: (theme: ThemeName) => void };
export const useThemeStore = create<ThemeStore>()((set) => ({
    theme: getSiteContext().theme,
    setTheme: (theme) => set({ theme }),
}));
