import React from 'react';
import { createRoot } from 'react-dom/client';
import { RouterProvider } from 'react-router-dom';
import 'antd/dist/reset.css';
import 'streamdown/styles.css';
import '../styles/globals.css';
import { AppProviders } from '@/components/layout/app-providers';
import { router } from '@/router';
import { applySiteContext } from './sync-context';

applySiteContext();
window.addEventListener('message', (event: MessageEvent) => {
    if (event.source === window.parent && event.origin === window.location.origin && event.data?.type === 'new-api:canvas-context') applySiteContext();
});

createRoot(document.getElementById('root')!).render(
    <React.StrictMode><AppProviders><RouterProvider router={router} /></AppProviders></React.StrictMode>,
);
