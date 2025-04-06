import React, { useContext } from 'react';
import TokensTable from '../../components/TokensTable';
import { Banner, Layout } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';
import { StatusContext } from '../../context/Status';

const Token = () => {
  const { t } = useTranslation();
  const [statusState] = useContext(StatusContext);

  return (
    <>
      <Layout>
        <Layout.Header>
          <Banner
            type='warning'
            description={t('令牌无法精确控制使用额度，只允许自用，请勿直接将令牌分发给他人。')}
          />
          <Banner
            type='info'
            description={`使用中转API时，需要把 https://api.openai.com 修改为 ${statusState?.status?.server_address || ''}`}
          />
        </Layout.Header>
        <Layout.Content>
          <TokensTable />
        </Layout.Content>
      </Layout>
    </>
  );
};

export default Token;
