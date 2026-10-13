import i18n from '@/i18n';
import { encodeChannelModel, modelOptionsFromChannels, useConfigStore } from '@/stores/use-config-store';
import { useThemeStore } from '@/stores/use-theme-store';
import { getSiteContext } from './site-context';

let previousSelection = '';

export function applySiteContext(): void {
    const context = getSiteContext();
    useThemeStore.setState({ theme: context.theme });
    void i18n.changeLanguage(context.language.startsWith('zh') ? 'zh-CN' : 'en-US');
    const current = useConfigStore.getState().config;
    const models = modelOptionsFromChannels(context.channels);
    const selected = encodeChannelModel(context.group, context.model);
    const selectedCapability = context.channels.find((channel) => channel.id === context.group)?.models.find((model) => model.name === context.model)?.capability;
    const next = { ...current, channels: context.channels, models, proxyEnabled: false, apiKey: '', baseUrl: window.location.origin };
    for (const capability of ['image', 'video', 'text', 'audio'] as const) {
        const key = `${capability}Model` as const;
        const valid = context.channels.some((channel) => channel.models.some((model) => model.capability === capability && encodeChannelModel(channel.id, model.name) === next[key]));
        if (!valid) {
            const channel = context.channels.find((item) => item.id === context.group && item.models.some((model) => model.capability === capability)) ?? context.channels.find((item) => item.models.some((model) => model.capability === capability));
            const model = channel?.models.find((item) => item.capability === capability);
            next[key] = channel && model ? encodeChannelModel(channel.id, model.name) : '';
        }
    }
    if (selected !== previousSelection && (selectedCapability === 'image' || selectedCapability === 'video')) {
        next[`${selectedCapability}Model`] = selected;
        next.model = selected;
        previousSelection = selected;
    }
    if (context.agentGroup && context.agentModel) next.textModel = encodeChannelModel(context.agentGroup, context.agentModel);
    useConfigStore.setState({ config: next });
}
