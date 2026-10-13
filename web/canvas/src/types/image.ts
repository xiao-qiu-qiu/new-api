export type ReferenceImage = {
    id: string;
    name: string;
    type: string;
    dataUrl: string;
    url?: string;
    storageKey?: string;
    role?: "mask";
};

export type SavedReferenceImage = Pick<ReferenceImage, "url" | "storageKey" | "role">;
