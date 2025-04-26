//aihubmax
import React, { useEffect, useState } from 'react';
import { API, showError } from '../../helpers';
import { marked } from 'marked';
import { Layout } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';

const CustomerService = () => {
  const { t } = useTranslation();
  const [content, setContent] = useState('');
  const [contentLoaded, setContentLoaded] = useState(false);

  const displayContent = async () => {
    setContent(localStorage.getItem('customer_service') || '');
    const res = await API.get('/api/customer_service');
    const { success, message, data } = res.data;
    if (success) {
      let content = data;
      if (!data.startsWith('https://')) {
        content = marked.parse(data);
      }
      setContent(content);
      localStorage.setItem('customer_service', content);
    } else {
      showError(message);
      setContent('加载客服内容失败...');
    }
    setContentLoaded(true);
  };

  useEffect(() => {
    displayContent().then();
  }, []);

  return (
    <>
      {contentLoaded && content === '' ? (
        <>
          <Layout>
            <Layout.Header>
              <h3>{t('客服')}</h3>
            </Layout.Header>
            <Layout.Content>
              <p>{t('如需联系客服，请点击')}{' '}
                <a href="https://www.baidu.com/udocs/kf" target="_blank" rel="noopener noreferrer">
                  {t('这里')}
                </a>
              </p>
            </Layout.Content>
          </Layout>
        </>
      ) : (
        <>
          {content.startsWith('https://') ? (
            <iframe
              src={content}
              style={{ width: '100%', height: '100vh', border: 'none' }}
            />
          ) : (
            <div
              style={{ fontSize: 'larger' }}
              dangerouslySetInnerHTML={{ __html: content }}
            ></div>
          )}
        </>
      )}
    </>
  );
};

export default CustomerService; 